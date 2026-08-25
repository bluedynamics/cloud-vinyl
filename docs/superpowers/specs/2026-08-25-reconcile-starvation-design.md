# Design: Reconcile-Starvation durch synchrone Agent-Aufrufe (Issue #72)

**Datum:** 2026-08-25
**Issue:** [bluedynamics/cloud-vinyl#72](https://github.com/bluedynamics/cloud-vinyl/issues/72)
**Status:** Entschieden, bereit zur Umsetzung
**Vorgänger:** [VCL-Konvergenz pro Pod](2026-08-25-vcl-konvergenz-design.md) (#73, PR #77)

## Problem

Der VinylCache-Controller läuft mit einem einzigen Reconcile-Worker.
`SetupWithManager` setzt kein `MaxConcurrentReconciles`, also gilt der
controller-runtime-Default 1.

Ein Reconcile führt zwei synchrone, netzgebundene Phasen aus:

1. `observeVCLNames` fragt **sequenziell** je erreichbarem Pod den aktiven
   VCL-Namen beim Agent ab
2. `pushVCL` pusht **parallel** je Pod, jeweils mit innerer Retry-Schleife
   (`maxAttempts` 3, Backoff `attempt * backoffBase`, also 5s und 10s)

Der HTTP-Client des Agents hat einen Timeout von 30s
(`--agent-client-timeout`, Chart-Default `agentClientTimeout: "30s"`).

Solange ein Reconcile in diesen Phasen steht, läuft **kein anderer Reconcile
dieses Controllers**. Das betrifft insbesondere den Deletion-Pfad: `Reconcile`
behandelt Löschungen zwar an Position 2, direkt nach dem Laden des Objekts, aber
dieser Durchlauf muss erst an die Reihe kommen. Bis dahin bleibt der Finalizer
stehen und die VinylCache verschwindet nicht.

### Belege

Beobachtet in [Run 32778410506](https://github.com/bluedynamics/cloud-vinyl/actions/runs/32778410506)
(E2E-Test `scaling`, erster Vorfall mit der Catch-Diagnostik aus PR #68):

```yaml
deletionTimestamp: "2026-08-24T21:19:06Z"
finalizers: [vinyl.bluedynamics.eu/finalizer]
```

Der Finalizer stand 60s später noch. Im gesamten Fenster gibt es **keine Zeile
aus `handleDeletion`** und **kein `Reconciler error`** — der Deletion-Reconcile
lief also nicht, statt zu laufen und zu scheitern. Parallel dazu:

```
21:19:07  ERROR  VCL push failed, retrying   attempt 1
21:19:42  ERROR  VCL push failed, retrying
```

Der damalige Auslöser, Pushes gegen terminierte Pods mit veralteter IP, ist
durch PR #77 beseitigt (`reachable` filtert auf `Running && PodIP != ""`). Der
**Mechanismus** ist unverändert, und die Konvergenz-Arbeit hat die synchrone
Zeit pro Reconcile eher vergrößert, weil `observeVCLNames` hinzugekommen ist.

### Größenordnung

Drei Replicas, die *Running mit IP* sind, aber nicht antworten. Nicht der Fall
eines toten Pods — der liefert sofort `connection refused` — sondern eines
hängenden varnishd, einer blockierenden NetworkPolicy oder von Paketverlust.

| Variante | Observe (sequenziell) | Push (parallel je Pod) | Worker blockiert |
|---|---|---|---|
| heute | 3 × 30s = 90s | 3 × 30s + 15s ≈ 105s | **≈ 195s** |
| nur E1 | 3 × 3s = 9s | ≈ 105s | ≈ 114s |
| nur E2 | 90s | 30s | ≈ 120s |
| **E1 + E2** | 9s | 30s | **≈ 39s** |

Die 60s aus dem E2E-Fehlschlag sind ein Test-Timeout, keine Obergrenze des
tatsächlichen Verzugs.

## Entscheidungen

### E1. Die Beobachtungsphase bekommt einen eigenen, kurzen Timeout

`observeVCLNames` läuft nicht mehr im 30s-Budget des allgemeinen Agent-Clients,
sondern unter einem eigenen, deutlich kürzeren Timeout (Vorschlag: 3s pro Pod).

Begründung: die Abfrage ist ein GET auf einen lokalen Pod im selben Cluster.
Antwortet er nicht binnen weniger Sekunden, ist die Information „Hash unbekannt"
genauso brauchbar wie eine späte Antwort — die Spec zur VCL-Konvergenz legt
bereits fest, dass ein Fehlschlag den Pod zum Push-Ziel macht und ein
überflüssiger Push folgenlos ist, weil `pushVCL` idempotent ist.

Der 30s-Timeout bleibt für den Push, wo er hingehört: dort wird geschrieben,
und ein Abbruch mitten im VCL-Load ist teurer als ein langes Warten.

Die sequenzielle Ausführung bleibt, wie in der Konvergenz-Spec entschieden.
Mit 3s statt 30s je Pod verliert die Sequenzialität ihre Schärfe.

### E2. Die innere Retry-Schleife in `pushVCL` entfällt

`pushVCL` versucht heute je Pod bis zu `maxAttempts` mal mit Backoff. Das ist
seit der Konvergenz-Umstellung **doppelte Mechanik**: schlägt ein Push fehl,
trägt der Pod das gewünschte VCL weiterhin nicht, der nächste Reconcile findet
ihn erneut über `podsNeedingVCL` und pusht wieder.

Der Reconcile *ist* die Retry-Schleife. Die innere Schleife wiederholt dasselbe,
nur blockierend und ohne das Rate-Limiting der Workqueue.

Nach der Streichung führt ein Reconcile je Pod höchstens einen Push-Versuch aus.
Wiederholung übernimmt das bestehende `RequeueAfter`.

Das ist ausdrücklich **weniger** Code, kein Umbau. Insbesondere ist es *nicht*
die Auslagerung des Pushes in einen Hintergrund-Worker; siehe „Verworfene
Alternativen".

### E3. Was mit `spec.retry.maxAttempts` und `spec.retry.backoffBase` geschieht

Bestätigt im Review am 2026-08-25: `backoffBase` wird umgedeutet, `maxAttempts`
stillgelegt. Die beiden Alternativen (beide Felder unangetastet stilllegen;
Versuchszähler im Status führen) wurden erwogen und verworfen.

Beide Felder existieren in `api/v1alpha1/vinylcache_types.go` und steuern
ausschließlich die innere Schleife aus E2.

Vorschlag:

- `backoffBase` steuert künftig den Requeue-Abstand nach einem fehlgeschlagenen
  Push und ersetzt dort die hartkodierten 30s. Das Feld behält damit eine
  ehrliche, verwandte Bedeutung.
- `maxAttempts` wird als wirkungslos dokumentiert und zur Entfernung in der
  nächsten brechenden Version vorgemerkt. Ein Konvergenz-Reconciler soll nicht
  aufgeben; „nach N Versuchen aufhören" wäre für einen Operator das falsche
  Verhalten. Das Feld begrenzt heute ohnehin nur Versuche *pro Reconcile* und
  nie global, die versprochene Semantik gab es also nie.

Alternative, falls `maxAttempts` erhalten bleiben soll: Versuchszähler je Pod im
Status führen und darüber begrenzen. Das bringt zusätzlichen Status, zusätzliche
Schreibkonflikte und einen Zustand, aus dem sich der Controller ohne Eingriff
nicht mehr befreit. Nicht empfohlen.

### E4. `MaxConcurrentReconciles` bleibt vorerst 1

Eine Erhöhung entkoppelt verschiedene VinylCaches voneinander, hilft aber
innerhalb eines Caches nicht — Reconciles desselben Objekts werden ohnehin
serialisiert. Sie trifft zudem auf die Optimistic-Concurrency-Konflikte, die in
den Operator-Logs bereits auftreten
(`Operation cannot be fulfilled on vinylcaches...: the object has been modified`).

E1 und E2 senken die blockierte Zeit um eine Größenordnung. Erst wenn danach
noch Starvation über Cache-Grenzen hinweg messbar ist, lohnt die Diskussion.

## Architektur

Der Ablauf je Reconcile bleibt der aus der Konvergenz-Spec. Es ändern sich zwei
Zeitbudgets:

```
1. Pods listen: reachable / ready                        unverändert
2. VCL generieren aus ready                              unverändert
3. observeVCLNames(reachable)                            NEU: 3s je Pod statt 30s
4. pushVCL(podsNeedingVCL(...))                          NEU: ein Versuch je Pod
5. ClusterPeers schreiben                                unverändert
6. Requeue: 5s unkonvergiert / 5m konvergiert            unverändert
   nach Push-Fehler: backoffBase statt fester 30s        NEU (E3)
```

## Komponenten und Schnittstellen

| Ort | Änderung |
|---|---|
| `vcl_observe.go` | eigener, kurzer Kontext-Timeout je Abfrage (E1) |
| `vcl_push.go` | innere Retry-Schleife entfällt, ein Versuch je Pod (E2) |
| `vinylcache_controller.go` | Requeue nach Push-Fehler aus `backoffBase` (E3) |
| `agent_client.go` | keine Signaturänderung; der 30s-Client bleibt für den Push |
| `vinylcache_types.go` | Doku an `maxAttempts`: wirkungslos, zur Entfernung vorgemerkt (E3) |
| `SetupWithManager` | unverändert (E4) |

## Tests

Unit, mit Fake-Client und Mock-AgentClient. Jeder Test wird vor der
Implementierung gegen das alte Verhalten scheitern gesehen, wie in PR #77 und
der Konvergenz-Spec gehandhabt.

- ein Pod, dessen Hash-Abfrage in den kurzen Timeout läuft, wird zum Push-Ziel
  und blockiert den Reconcile nicht über das kurze Budget hinaus
- `pushVCL` führt je Pod genau einen Versuch aus; ein fehlgeschlagener Push
  erzeugt kein Warten innerhalb des Reconcile
- ein fehlgeschlagener Push führt beim nächsten Reconcile erneut zum Push
  desselben Pods, ohne dass sich der gewünschte Hash geändert hat
- der Requeue-Abstand nach einem Push-Fehler folgt `backoffBase`
- ein gesetzter `maxAttempts` verändert das Verhalten nicht mehr (E3)

Nicht als Unit-Test abbildbar und deshalb ausdrücklich benannt: dass ein
laufender Reconcile das Löschen einer anderen VinylCache verzögert. Der Nachweis
wäre ein Test mit zwei Objekten und einem künstlich hängenden Agent. Ob sich der
Aufwand lohnt, ist Teil des Reviews.

## Verworfene Alternativen

**Push in einen Hintergrund-Worker auslagern (Goroutine).** Kein Backpressure,
Races auf dem Status, Leaks beim Shutdown. Löst ein Problem, das E2 durch
Streichen beseitigt.

**Eigener, Pod-basierter Controller.** Konzeptuell die reinste Umsetzung von
„konvergiere pro Pod", aber ein zweiter Reconciler mit Ownership-, Reihenfolge-
und Statusfragen. Deutlich mehr Fläche als das Problem rechtfertigt.

**Parallelisierung von `observeVCLNames`.** Die Konvergenz-Spec hat das bereits
als spätere Option eingeordnet, mit dem Argument schwer testbarer Nebenläufigkeit
ohne heutigen Nutzen. E1 macht sie zusätzlich entbehrlich.

## Offene Punkte

1. ~~E3 braucht eine Entscheidung~~ — entschieden im Review am 2026-08-25.
2. Der konkrete Wert für den Observe-Timeout (Vorschlag 3s) ist gegriffen, nicht
   gemessen. Er sollte vor dem Merge einmal gegen einen realen Cluster geprüft
   werden — die Abfrage ist ein GET auf einen Pod im selben Cluster, 3s sind
   großzügig, aber die Zahl verdient eine Bestätigung statt eines Bauchgefühls.
3. Ob der E2E-Nachweis für die Verzögerung des Löschens gebaut wird, siehe Tests.
