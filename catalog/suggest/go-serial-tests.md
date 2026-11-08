---
id: go-serial-tests
kind: tip
title: Keep the machine responsive during Go tests
tags: [testing, performance]
applies_to:
  ecosystems: [go]
since: 1.0.0
---
With several agents on one machine, `GOMAXPROCS=1 go test -p 1 ./...` runs one
package at a time on one thread. The tests take longer and the machine stays
usable.
