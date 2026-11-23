/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package argocd tests the Argo CD health check for VinylCache, which users
// add to argocd-cm (#189). It has no other code.
package argocd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	lua "github.com/yuin/gopher-lua"
	"sigs.k8s.io/yaml"
)

const healthScript = "../../config/argocd/vinylcache-health.lua"

// health runs the script the way Argo CD does: a gopher-lua state with only
// the safe standard libraries, the resource as the global obj, and the table
// the script returns.
func health(t *testing.T, resource string) (status, message string) {
	t.Helper()
	script, err := os.ReadFile(healthScript)
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(resource), &obj))

	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{{lua.BaseLibName, lua.OpenBase}, {lua.TabLibName, lua.OpenTable}, {lua.StringLibName, lua.OpenString}, {lua.MathLibName, lua.OpenMath}} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	L.SetGlobal("obj", toLua(L, obj))
	require.NoError(t, L.DoString(string(script)))
	hs, ok := L.Get(-1).(*lua.LTable)
	require.True(t, ok, "the script must return a table")
	return lua.LVAsString(hs.RawGetString("status")), lua.LVAsString(hs.RawGetString("message"))
}

// toLua converts a decoded YAML value into Lua values: maps to tables with
// string keys, lists to tables with 1-based integer keys.
func toLua(L *lua.LState, v any) lua.LValue {
	switch v := v.(type) {
	case map[string]any:
		t := L.NewTable()
		for k, item := range v {
			t.RawSetString(k, toLua(L, item))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, item := range v {
			t.Append(toLua(L, item))
		}
		return t
	case string:
		return lua.LString(v)
	case bool:
		return lua.LBool(v)
	case float64:
		return lua.LNumber(v)
	case nil:
		return lua.LNil
	default:
		panic("unexpected YAML value")
	}
}

func TestHealth(t *testing.T) {
	cases := []struct {
		name, resource, status, message string
	}{
		{"stopped is suspended", `
status: {phase: Stopped}`, "Suspended", "scaled to zero replicas"},
		{"ready is healthy", `
status: {phase: Ready}`, "Healthy", "VinylCache is ready"},
		{"degraded names the backend without endpoints", `
status:
  phase: Degraded
  conditions:
  - {type: Ready, status: "True", message: VCL active on 2/2 ready pods}
  - {type: BackendsAvailable, status: "False", message: backend plone has no endpoints}`, "Degraded", "backend plone has no endpoints"},
		{"degraded prefers the status message", `
status: {phase: Degraded, message: VCL push failed on all 2 pods}`, "Degraded", "VCL push failed on all 2 pods"},
		{"error without a message names the phase", `
status: {phase: Error}`, "Degraded", "Error"},
		{"pending is progressing with the Ready message", `
status:
  phase: Pending
  conditions:
  - {type: Ready, status: "False", message: waiting for 1/2 replicas to become ready}`, "Progressing", "waiting for 1/2 replicas to become ready"},
		{"no status yet is progressing", `
metadata: {name: c1}`, "Progressing", "Waiting for VinylCache to become ready"},
		{"without a phase the Ready condition decides", `
status:
  conditions:
  - {type: Ready, status: "False", message: no pods}`, "Degraded", "no pods"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, message := health(t, c.resource)
			assert.Equal(t, c.status, status)
			assert.Equal(t, c.message, message)
		})
	}
}
