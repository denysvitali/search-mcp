# Captured SERP fixtures

Real responses captured from the live upstream, kept so the parsers are tested
against actual markup rather than hand-written approximations — upstream markup
drift is the failure mode most likely to quietly degrade this tool.

Session-derived values have been redacted: anti-bot fingerprint parameters,
page-view/tracking identifiers, cookie values and per-request link signatures
are replaced with `REDACTED`. Yahoo's `<script>` blocks are stripped entirely,
since that is where its tracking payloads live. The structure the parsers rely
on — results containers, result rows, and the `RU=` destination URLs that get
unwrapped — is untouched.

When refreshing a fixture, re-run the same redaction before committing.
