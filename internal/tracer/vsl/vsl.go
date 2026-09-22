// Package vsl parses varnishlog -g request text output into transaction
// groups. It is deliberately independent of the OTel model: it knows tags,
// payloads and nesting, nothing about spans.
package vsl

import (
	"bufio"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Record is a single VSL log line's tag and payload, stripped of its marker
// and star/dash depth prefix.
type Record struct{ Tag, Payload string }

// Tx is one varnishlog transaction group (a "*"-prefixed block): a request,
// backend request, or session, with the records logged directly under it and
// any nested groups (e.g. a BeReq fetched to satisfy a Request miss).
type Tx struct {
	Type     string
	VXID     uint64
	Records  []Record
	Children []*Tx
}

var (
	groupRe  = regexp.MustCompile(`^(\*+)\s+<<\s+(\w+)\s+>>\s+(\d+)\s*$`)
	recordRe = regexp.MustCompile(`^(-+)\s+(\S+)\s*(.*)$`)
)

// Parser reads varnishlog -g request text output and yields one Tx per
// top-level transaction group via Next.
type Parser struct {
	s *bufio.Scanner
	// byLevel[i] is the most recent Tx opened at star-depth i+1.
	byLevel []*Tx
	pending *Tx
	eof     bool
}

// NewParser returns a Parser reading varnishlog -g request text from r.
func NewParser(r io.Reader) *Parser {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Parser{s: s}
}

// Next returns the next completed top-level transaction group. A group is
// complete at the blank line that varnishlog prints after it, or at EOF.
func (p *Parser) Next() (*Tx, error) {
	if p.eof {
		return nil, io.EOF
	}
	for p.s.Scan() {
		line := p.s.Text()
		if strings.TrimSpace(line) == "" {
			if p.pending != nil {
				tx := p.pending
				p.pending = nil
				p.byLevel = nil
				return tx, nil
			}
			continue
		}
		if m := groupRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			vxid, _ := strconv.ParseUint(m[3], 10, 64)
			tx := &Tx{Type: m[2], VXID: vxid}
			if level == 1 {
				// A new top-level group before a blank line: flush the old one.
				if p.pending != nil {
					old := p.pending
					p.pending = tx
					p.byLevel = []*Tx{tx}
					return old, nil
				}
				p.pending = tx
				p.byLevel = []*Tx{tx}
			} else if level-2 < len(p.byLevel) {
				parent := p.byLevel[level-2]
				parent.Children = append(parent.Children, tx)
				p.byLevel = append(p.byLevel[:level-1], tx)
			}
			continue
		}
		if m := recordRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if level-1 < len(p.byLevel) {
				t := p.byLevel[level-1]
				t.Records = append(t.Records, Record{Tag: m[2], Payload: m[3]})
			}
			continue
		}
		// Anything else (notices, truncation markers) is skipped, never fatal.
	}
	p.eof = true
	if p.pending != nil {
		tx := p.pending
		p.pending = nil
		return tx, nil
	}
	if err := p.s.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// Has reports whether t has at least one record with the given tag.
func (t *Tx) Has(tag string) bool {
	_, ok := t.First(tag)
	return ok
}

// First returns the payload of the first record with the given tag.
func (t *Tx) First(tag string) (string, bool) {
	for _, r := range t.Records {
		if r.Tag == tag {
			return r.Payload, true
		}
	}
	return "", false
}

// Timestamp finds a "Timestamp <label>: <epoch> ..." record and returns the
// absolute time. VSL epochs are fractional seconds.
func (t *Tx) Timestamp(label string) (time.Time, bool) {
	prefix := label + ": "
	for _, r := range t.Records {
		if r.Tag != "Timestamp" || !strings.HasPrefix(r.Payload, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(r.Payload, prefix))
		if len(fields) == 0 {
			return time.Time{}, false
		}
		epoch, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return time.Time{}, false
		}
		sec, frac := math.Modf(epoch)
		return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
	}
	return time.Time{}, false
}

// Header returns the value of "<tag> <name>: <value>", name compared
// case-insensitively (HTTP header names are).
func (t *Tx) Header(tag, name string) (string, bool) {
	for _, r := range t.Records {
		if r.Tag != tag {
			continue
		}
		k, v, ok := strings.Cut(r.Payload, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
