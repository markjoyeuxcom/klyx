# S8a lab findings (Alertmanager 0.34.1 on kind-klyx, 2026-10-19)

1. Time-interval muting is reported only by /api/v2/alerts/groups.
   - The same muted alert (route with mute_time_intervals [always], the
     receiver has an integration) reads, at the same moment:
       /api/v2/alerts         state=active, mutedBy=[]   (field present, empty)
       /api/v2/alerts/groups  state=suppressed, mutedBy=["always"], receiver lab-muted
   - Muting is per route/group, so the flat alert list cannot carry it. A
     present but empty mutedBy on /api/v2/alerts does not mean "not muted".
   - The mute stage runs in a receiver's notify pipeline: a receiver with
     no integration never marks a mute (the lab routes the muted alert to a
     discard-port webhook that is never called).
2. Silences and inhibition are reported on both endpoints:
     KlyxLabSilenced  suppressed, silencedBy=[<silence id>]
     KlyxLabWarning   suppressed, inhibitedBy=["0a05fbb640de8506"] (KlyxLabCritical's fingerprint)
3. filter=cluster="kind-klyx" is applied server-side (11 of 12; cluster=elsewhere excluded).
4. A route prefix works through the API server's service proxy:
   /api/v1/namespaces/klyx-s8-lab/services/http:alertmanager-prefixed:web/proxy/alertmanager/api/v2/alerts
5. /api/v2/status: version 0.34.1, cluster.status "disabled" with
   --cluster.listen-address= (no peers).
6. kubectl create --raw POST through the service proxy gets 415 from
   Alertmanager (not application/json); the lab posts with amtool inside
   the container instead.
7. A partly muted alert (two routes for klyx_lab_partmute, the first muted
   with continue: true, the second not) reads on both endpoints:
     /api/v2/alerts         active, mutedBy=[], receivers [lab-muted, lab]
     /api/v2/alerts/groups  lab-muted: suppressed, mutedBy=["always"]
                            lab:       active, mutedBy=[]
   The groups' receiver multiset equals the flat receivers, so Klyx reads it
   partial: muted for lab-muted, active for lab, and counted.
8. Lab maintenance: the lab silence must outlive a smoke run. A 3h silence
   checked "still active" by a 17:12 relabs expired at 17:16, mid-smoke:
   KlyxLabSilenced turned active (8 counted, not 7), the smoke's state check
   waited its 90 s and the runs past the 5-minute report limit came back
   "incomplete" (day modes) or failed (night). alerts.sh now expires the
   lab silences and adds a fresh 24h one on every apply.
