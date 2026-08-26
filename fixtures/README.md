# Fixtures

Parser test fixtures live here. The layout is:

```text
fixtures/
  logs/       real ev_*.xml / ev_*.zxml round logs (inputs)
  expected/   expected parse output per log (golden files)
```

Both directories are currently empty placeholders — the parser's unit tests
(`internal/parser/bflog/parser_test.go`) use inline XML instead. If you have a
real round log you're comfortable sharing (nicknames and chat lines are
visible in it, so sanitize first), a fixture-based compatibility test is a
welcome contribution:

```text
fixtures/
  logs/
    ev_14567-20040902_1453.xml
  expected/
    ev_14567-20040902_1453.events.json
    ev_14567-20040902_1453.summary.json
```

Do not assume every Battlefield event is supported just because it appears in
a log. The legacy select(bf) parser recognized only a subset and ignored
events such as `destroyPlayer`, `destroyVehicle`, `radioMessage`, `setTeam`,
and `spawnEvent`; this parser handles more, but coverage is driven by what
shows up in real logs.
