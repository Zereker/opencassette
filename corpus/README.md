# corpus/ — the recordings

```
corpus/<vendor>/<model>/<protocol>/<stream|nostream>/<scenario>.yaml
```

Every file here is a real capture: recorded with `opencassette record`
(carrying its `meta:` provenance block), or imported from a third-party
project's published cassettes with license and provenance documented in the
importing PR. `opencassette verify corpus` runs in CI on every change;
see [CONTRIBUTING.md](../CONTRIBUTING.md) for what a recording PR must
satisfy — the short version is *real, scrubbed, provenanced*.

The corpus starts small on purpose. First targets are the vendor ecosystems
where no public recorded traffic exists at all (DeepSeek, Zhipu GLM,
MiniMax); one scenario-pack run per model is the intended unit of
contribution.
