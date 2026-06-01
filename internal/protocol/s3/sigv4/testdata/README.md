# AWS SigV4 Test Suite (vendored)

These fixtures drive `internal/protocol/s3/sigv4/` unit tests. They are
**vendored into the repo** so the air-gapped build/test loop never needs a
network fetch (the project's air-gap constraint — zero outbound network
calls at runtime — extended here to zero outbound calls at test time).

## Source

Primary:
`https://docs.aws.amazon.com/general/latest/gr/samples/aws4_testsuite.zip`

AWS retired that direct URL sometime in 2024; the copy vendored here was
fetched from the Wayback Machine snapshot of that artifact on 2026-04-16
(`https://web.archive.org/web/2023if_/https://docs.aws.amazon.com/general/latest/gr/samples/aws4_testsuite.zip`).
The archive contents are dated 2012-03-08 — the test vectors have not
changed since then; they are the canonical reference fixtures referenced
by every AWS SigV4 implementation review (AWS docs "Examples of signed
requests using Signature Version 4").

## License

AWS publishes these fixtures as documentation samples. AWS SDK and
documentation samples are redistributable under the
[Apache License 2.0](https://github.com/aws/aws-sdk-go-v2/blob/main/LICENSE.txt)
— compatible with our corporate Apache-2.0-only rule.

## Test cases vendored (minimum set)

Each subdirectory contains the 5 fixture files AWS publishes per case:
`.req` (the raw HTTP request), `.creq` (canonical request),
`.sts` (string-to-sign), `.authz` (Authorization header),
`.sreq` (signed request).

| Directory | Scenario |
|-----------|----------|
| `aws4_testsuite/get-vanilla/`                          | Plain GET with required headers only |
| `aws4_testsuite/post-vanilla/`                         | Plain POST (empty body) |
| `aws4_testsuite/post-vanilla-query/`                   | POST with query-string parameters |
| `aws4_testsuite/get-header-value-trim/`                | Header whitespace normalization |
| `aws4_testsuite/post-x-www-form-urlencoded-parameters/`| Signed form body hash |

Additional upstream cases (`get-vanilla-query`, `get-unreserved`,
`get-utf8`, `post-header-key-sort`, etc.) are intentionally NOT vendored
to keep the testdata footprint minimal; add selectively if a missed edge
case turns up.

## Fetching additional cases offline

The upstream zip archive is already attached to this commit's Bash
history; to rehydrate it on a new machine without network, ask the
maintainer for a copy rather than re-downloading from AWS.
