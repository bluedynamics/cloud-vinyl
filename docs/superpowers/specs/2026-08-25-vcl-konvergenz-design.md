# Design: VCL-Konvergenz pro Pod (Issues #73, PR #77)

Status: Entwurf, wartet auf Review
Anlass: drei aufeinanderfolgende E2E-Fehlschläge in PR #77, jeweils andere
Ursache, jeweils dieselbe zugrundeliegende Eigenschaft.

## Problem

Der Reconciler entscheidet über den VCL-Push aus **aggregierten** Signalen:

```go
if genResult.Hash != activeHash || len(peers) != len(vc.Status.ClusterPeers) {
    r.pushVCL(ctx, vc, genResult, targets)
}
```

Das sind ein Hash für den gesamten Cache und zwei Zählerstände. Die Frage, die
beantwortet werden muss, ist aber eine pro Pod: **hat dieser erreichbare Pod das
gewünschte VCL bereits?**

Solange jeder Pod sofort Ready wurde, fiel das nicht auf. Genau das tat er, weil
`ActiveVCL` im Agent `vcl.list` falsch parste und nie `"boot"` zurückgab, wodurch
die Readiness-Probe wirkungslos war (#73). Der Bug war tragend: er hielt drei
verschiedene Mechanismen am Laufen, die sich alle auf ihn stützten.

### Belege aus PR #77

| Runde | Symptom | Aggregat, das versagte | Fix |
|---|---|---|---|
| 1 | `VCL pushed to 0/N pods`, alle 7 Pod-Tests rot | Ready-Filter auf der Zielliste | Push-Ziele nach Erreichbarkeit statt Readiness |
| 2 | genau ein Reconcile-Log pro Cache, danach Stille | ein Hash für den ganzen Cache, nach Null-Pod-Push gesetzt | `ActiveVCL` nur bei tatsächlichem Push setzen |
| 3 | Replica 0 Ready, 1 und 2 dauerhaft `1/2` | Peer-**Zahl** statt Peer-**Zustand** | offen, Gegenstand dieser Spec |

Nach Runde 3: 7 von 8 E2E-Tests grün. Der verbleibende Fehlschlag ist ein
Cache mit drei Replicas. Sobald Pod 0 Ready ist, gilt `len(peers) == 1` und
`len(ClusterPeers) == 1`, der Hash ist stabil, die Bedingung wird falsch. Die
Pods 1 und 2 sind erreichbar, tragen kein VCL und werden nie wieder adressiert.
Im Log steht ab dann nur noch `VCL already loaded, skipping`.

## Vorhandene, ungenutzte Bausteine

Der Code hat die Teile für die richtige Lösung bereits, sie sind nur nicht
verdrahtet:

- `ClusterPeerStatus` modelliert Zustand pro Pod, inklusive `Ready bool` und
  `ActiveVCLHash string`. Der Controller füllt die Liste jedoch ausschließlich
  aus Ready-Pods und setzt `Ready: true` hartkodiert, wodurch das Feld keine
  Information trägt.
- `AgentClient.ActiveVCLHash(ctx, namespace, podIP)` ist deklariert und
  implementiert, wird aber **nirgends aufgerufen**.
- `pushVCL` ist bereits idempotent, es wertet `Already a VCL named` als Erfolg.
- Die Alerting-Regel `VinylCacheVCLDrift` existiert, aber nichts speist sie.
  `status.go` vermerkt dazu ausdrücklich "intentionally out of scope here".

## Entscheidungen

**E1. Die Push-Entscheidung wird pro Pod getroffen.** Gepusht wird an jeden
erreichbaren Pod, dessen beobachteter VCL-Hash vom gewünschten abweicht. Der
globale Hash-Vergleich entfällt als Auslöser und bleibt nur noch Reporting.

**E2. Der beobachtete Hash wird beim Agent erfragt, nicht aus dem Status
gelesen.** Begründung: ein Pod, dessen varnishd neu startet, verliert sein VCL
und fällt auf `boot` zurück, behält aber seinen Namen. Ein im Status
gespeicherter Hash wäre dann falsch, der Pod bekäme nie wieder ein VCL, und wir
hätten die vierte Instanz desselben Musters. Die Abfrage ist selbstheilend.

Kosten: eine HTTP-Anfrage je erreichbarem Pod und Reconcile. Im konvergierten
Zustand läuft der Reconcile alle 5 Minuten, während der Konvergenz alle 5
Sekunden für wenige Durchläufe. Bei üblichen Replica-Zahlen ist das
vernachlässigbar.

Schlägt die Abfrage fehl, gilt der Hash als unbekannt und es wird gepusht. Der
Push ist idempotent, ein überflüssiger Push ist also folgenlos.

**E3. `ClusterPeers` listet alle erreichbaren Pods, mit echten Werten.**
`Ready` bekommt den tatsächlichen Zustand statt einer Konstanten,
`ActiveVCLHash` den tatsächlich beobachteten Hash. Das ist genau das, was der
Typ dokumentiert. Es ist eine reine Status-Änderung, kein Bruch am Schema, aber
es verändert, was Nutzer sehen: nicht bereite Pods tauchen künftig auf.

**E4. Drift-Detection fällt dabei ab.** Sobald der beobachtete Hash je Pod
vorliegt, ist Drift die Menge der Pods, deren Hash vom gewünschten abweicht,
obwohl sie zuletzt gepusht wurden. Ob die Metrik in derselben Änderung
befüllt wird, ist eine offene Frage (siehe unten).

## Architektur

### Ablauf je Reconcile

```
1. Pods listen
      reachable = Running && PodIP != ""      -> Push-Ziele
      ready     = reachable && PodReady       -> Traffic, Shard-Peers
2. VCL generieren aus ready
3. Für jeden Pod in reachable: beobachteten Hash beim Agent erfragen
4. Pushen an { p in reachable | observed[p] != desired }
5. ClusterPeers aus reachable schreiben, mit echtem Ready und echtem Hash
6. Requeue: 5s solange nicht alle Replicas ready, sonst 5m
```

### Konvergenzbeweis

Jeder Durchlauf pusht genau an die Pods, denen das gewünschte VCL fehlt. Ein Pod,
der es hat, wird übersprungen. Haben alle erreichbaren Pods es, findet kein Push
mehr statt, und der Zustand ist stabil.

Der gewünschte Hash ändert sich, wenn sich die Ready-Menge ändert, weil die
Peers in den Shard-Director eingehen. Das ist ein endlicher Prozess: mit jedem
Pod, der Ready wird, ändert sich der Hash einmal, danach nicht mehr. Ein Pod
bleibt beim Neuladen des VCL Ready, weil die Readiness nur auf `name != "boot"`
prüft und der neue Name `<ns>-<cache>-<hash8>` lautet. Es gibt also keine
Oszillation zwischen Ready und NotReady.

Startet ein Pod neu, fällt sein beobachteter Hash auf den des Bootstrap-VCL
zurück, er wird im nächsten Durchlauf erneut bepusht und heilt von selbst. Das
ist der Fall, den eine im Status gespeicherte Buchführung nicht abdeckt.

## Komponenten und Schnittstellen

| Ort | Änderung |
|---|---|
| `collectPeers` | bleibt wie in PR #77: liefert `reachable` und `ready` |
| neu: `observeVCLHashes` | fragt `AgentClient.ActiveVCLHash` für alle `reachable` ab, parallel, mit Timeout je Pod; Fehler ergeben einen leeren Hash |
| `vinylcache_controller.go` | Push-Bedingung entfällt, stattdessen wird die Zielmenge aus dem Hash-Vergleich gebildet |
| `pushVCL` | unverändert, bekommt nur eine kleinere Zielmenge |
| `updateStatus` | `ClusterPeers` aus `reachable` mit echten Werten; `ActiveVCL` weiterhin nur bei tatsächlichem Push |
| `AgentClient` | keine Signaturänderung, die Methode wird endlich benutzt |

## Tests

Unit, mit Fake-Client und Mock-AgentClient:

- Pod mit abweichendem Hash wird gepusht, Pod mit passendem nicht
- Pod, dessen Hash-Abfrage fehlschlägt, wird gepusht
- ein neu hinzukommender Pod wird gepusht, obwohl sich der gewünschte Hash nicht
  geändert hat. Das ist genau der Fall aus Runde 3
- ein Pod, der auf den Bootstrap-Hash zurückfällt, wird erneut gepusht
- `ClusterPeers` enthält nicht bereite Pods mit `ready: false`
- konvergierter Zustand löst keinen Push aus

E2E: der bestehende `scaling`-Test mit drei Replicas deckt Runde 3 ab und muss
grün werden. Ein zusätzlicher Fall für den Pod-Neustart wäre wünschenswert.

Jeder Test wird vor der Implementierung gegen das alte Verhalten scheitern
gesehen, wie in PR #77 durchgehend gehandhabt.

## Vor der Implementierung zu klären

1. **E2 bestätigen.** Abfrage je Reconcile statt Status-Buchführung. Der
   Selbstheilungsvorteil ist der Grund, die Kosten sind N Requests. Falls N groß
   werden kann, wäre ein Mittelweg denkbar: Status als Cache, Abfrage nur bei
   Verdacht. Das ist mehr Mechanik und Komplexität und erst dann gerechtfertigt,
   wenn N wirklich groß wird.
2. **Drift-Metrik in derselben Änderung oder separat?** Sie fällt technisch ab,
   vergrößert aber den Diff und hat eigene Testfläche.
3. **Sichtbarkeitsänderung an `ClusterPeers`.** Nicht bereite Pods erscheinen
   künftig im Status. Fachlich richtig, aber eine wahrnehmbare Änderung für
   jeden, der die Liste auswertet.

## Bewusst nicht im Umfang

- Das Readiness-Konzept selbst. Die Probe bleibt wie sie ist: nicht ready,
  solange das Bootstrap-VCL aktiv ist. Sie ist der Teil, der Nutzer davor
  schützt, auf einen Pod geroutet zu werden, der 503 ausliefert.
- Die drei Commits aus PR #77. Jeder ist für sich begründet und keiner ist
  falsch, sie sind nur nicht hinreichend. Sie bleiben Grundlage dieser Arbeit.
- Startup- statt Readiness-Probe. Würde den Zyklus nicht auflösen, weil auch
  eine Startup-Probe den Pod von den Endpoints fernhält.
