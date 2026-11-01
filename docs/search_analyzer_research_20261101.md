# Chinese analyzer research

Date: 2026-11-01

Status: Deferred at the user's request. Keep the current search implementation.
Chinese analysis is an optional relevance optimization, not an implementation
task or a prerequisite for search. SmartCN remains a candidate, not an adopted
architecture. The study changed no application code or production service.

## Current behavior and the question

Search splits input on Unicode whitespace and uses case-insensitive literal
substrings within one document's title or body. Results rank by distinct input
keyword coverage, ASCII identifier-boundary coverage, and creation time. All
keyword matches therefore precede partial matches in one paginated result stream.
There is no Chinese analyzer in the application mapping.

The motivating counterexample is a Huawei query against a sentence meaning
"For toothpaste I only choose Zhonghua; why?" The literal Huawei characters
span the end of Zhonghua and the start of "why". The current substring search
finds them, and the ASCII boundary test cannot detect the Chinese word boundary.

Exact inputs, written as Unicode escapes to keep repository text ASCII:

```json
{
  "query": "\u534e\u4e3a",
  "text": "\u7259\u818f\u6211\u53ea\u9009\u4e2d\u534e\u4e3a\u4ec0\u4e48\uff1f"
}
```

## Experiment and evidence

The isolated runtime was Elasticsearch 9.5.5 / Lucene 10.5.1 with SmartCN,
ICU, and IK plugins at 9.5.5. The IK archive matched its vendor SHA512 and
Elasticsearch version descriptor. All data was synthetic; the owned Docker
container, network, volume, and derived image were removed afterward.

The matrix covered 24 research-owner documents and one foreign-owner negative,
21 queries, and seven analysis configurations: Standard, CJK, SmartCN, ICU,
SmartCN without Porter stemming, `ik_smart`, and `ik_max_word`.

- 245 ordinary analyze calls: seven analyzers times 35 input texts.
- Seven additional explain calls captured analysis stages.
- 1,092 native search comparisons: 21 queries times 52 query strategies.
- A separate 21-search prototype tested literal recall with analyzed phrase
  quality. Mapping and version-migration probes were also separate.

The study compared native `most_fields`, `combined_fields`, phrase, `dis_max`,
and a low-weight wildcard fallback. Query operator matters: an OR result must
not be reported as an AND result. These are small functional examples, not a
production performance benchmark or a general Chinese accuracy evaluation.
The toy owner filter was `owner=research`; its negative fixture is not an
end-to-end Manager/PostgreSQL authorization test.

Durable artifacts are in
[research/search_analyzers_20261101](research/search_analyzers_20261101/):

- [evidence.json](research/search_analyzers_20261101/evidence.json) preserves
  runtime/plugin provenance, corpus, complete analyze responses, compact native
  match IDs/scores, focused outcomes, prototype requests/results, and migration
  responses. Native search response metadata and repeated source bodies are
  omitted from the compact match rows.
- [experiment.py](research/search_analyzers_20261101/experiment.py) reproduces
  the main matrix against a fresh disposable ES instance with the three plugins.
  It writes toy indexes and must not target production or an existing data index.

## Findings

### The toothpaste counterexample

| Analyzer | Relevant emitted words, expressed as English glosses | Huawei query matches the sentence with native combined-fields AND |
| --- | --- | --- |
| SmartCN | Zhonghua / why | No |
| ICU | Zhonghua / why | No |
| IK smart | choose-middle / Huawei / what | Yes |
| IK max word | Includes Zhonghua, Huawei, why, and what | Yes |
| CJK | Includes overlapping two-character Huawei token | Yes |
| Standard | Individual Chinese characters include both query characters | Yes |

SmartCN and ICU avoided this exact counterexample. Neither IK mode did.
Finer tokenization is not inherently more accurate, and no analyzer is an
infallible semantic boundary detector. Literal substring recall still includes
the sentence; an analyzer can reduce its ranking relative to genuine word hits.

### Other behavior changes

| Input or query | SmartCN | ICU | Consequence |
| --- | --- | --- | --- |
| Database noun, `\u6570\u636e\u5e93` | One database token | Data / library tokens | A data query matches it as a token only with ICU; wildcard keeps recall with either. |
| Apple notebook, `\u82f9\u679c\u7b14\u8bb0\u672c` | Apple / notebook | Apple / notes / final-character tokens | A final-character query needs substring fallback with SmartCN. |
| `port_name` | `port`, `name` | `port_name` | SmartCN would promote `port` in this identifier; that differs from today's underscore boundary rule. |
| `port` against `transport` | No token match | No token match | Substring recall can retain the result at lower quality. |
| `C++` and `C` | Both analyze to `c` | Both analyze to `c` | Analyzer-only recall can conflate them. A literal gate preserves `C++` as literal input. |
| `ports` against `port` | Matches through English stemming | No token match | Native analysis can broaden recall if allowed to replace the literal gate. |
| `db` against the Chinese database noun | No token match | No token match | Segmentation does not supply cross-language acronym semantics. |

IK smart preserved `c++` as one token. IK max emitted both `c++` and `c`, so
whether a C-only document matches depends on the query operator. Exact tokens,
positions, and UTF-16 offsets are preserved in the evidence.

## Candidate change if optimization resumes

The tentative preference is SmartCN for the tested Chinese noun examples, with
ICU as the mixed-language alternative. The identifier, punctuation, and stemming
differences above require explicit acceptance and broader examples before choice.
Do not add per-word exceptions for Huawei, port, or individual brands.

The smallest compatible change would retain wildcard literal recall and replace
only the ASCII boundary-quality predicate with analyzed phrase matching on
additional text fields. Keep each analyzed quality predicate gated by the same
literal keyword. This retains short Chinese substrings and literal punctuation
without admitting analyzer-only false candidates.

For N distinct raw input keywords, the research prototype used:

```text
L(t) = literal substring in title OR body
W(t) = L(t) AND (analyzed phrase in title words OR body words)
score = (N + 1) * count(L) + count(W)
```

At least one L must match. This preserves the agreed coverage-first policy:
three substring keywords still outrank two word-quality keywords. Within equal
coverage, analyzed word quality replaces the handwritten ASCII test. Production
would retain its time and stable pagination tie-breakers; the prototype used
score/document ID only and did not test PIT pagination.

The 21 prototype searches confirmed genuine Huawei results outrank the toothpaste
sentence with SmartCN/ICU, substring-only Chinese results remain available, a
`C++` query excludes a C-only document, and cross-title/body full coverage remains
ahead of partial coverage. IK smart still rewards the counterexample.

The prototype is not BM25 ranking. Native BM25, ordinary boosts, and `dis_max`
do not automatically guarantee strict full-coverage-before-partial ordering.
Making BM25 the primary ranking would be a separate product-policy change.

Owner/deleted filters, current PG permission and visibility checks, canonical
message identities, part assembly, source versions, and summary references remain
independent of analyzer selection. Allowing nonliteral stemming recall would also
require deliberate changes to current PG literal-match revalidation.

## Migration constraints

Both text-primary/wildcard-subfield and wildcard-primary/text-subfield mappings
worked in ES 9.5.5 without changing `_source`. Adding a text subfield to an
existing populated wildcard mapping did not populate old documents; new writes
populated it immediately. Installing a plugin alone is insufficient.

Do not apply scriptless `_update_by_query` to the current application index as a
shortcut. The isolated proof started at strict external version 100, rewrote the
document, and observed ES version 101. The next real external-101 write returned
409 while retaining the old body. The current writer uses strict external
versioning and treats version conflicts as success, so this can advance the PG
acknowledgment without indexing the new source content.

Two limited version-preserving alternatives were demonstrated:

1. Rewriting the exact captured full `_source` with `external_gte` at its captured
   version 100 preserved version 100 and populated tokens. A normal strict-101
   update then succeeded; replaying stale version 100 returned 409. This does not
   justify arbitrary equal-version content replacement or changing the app writer.
2. `_reindex` to a new destination with `version_type: external` preserved version
   100 and later 101. Self-reindex was rejected. This preserves the current ES
   version; it does not repair a version already inflated by an unsafe rewrite.

Neither probe establishes a production migration. Concurrent catch-up, cutover,
deletion/tombstone propagation, retries, progress, and operational load still need
a plan. Changing `SEARCH_INDEX` alone is insufficient: current queue backfill
inserts missing rows and does not replay already acknowledged work for a new
index generation. Likewise, `EnsureIndex` does not upgrade an existing mapping.

Punctuation-only input produced zero SmartCN tokens while `exists(body.words)`
was true. Field-existence counts cannot certify useful token population or
migration completion. Analyzer or ranking changes also require a new cursor
fingerprint version; the current fixed-integer score validation cannot be reused
unchanged for arbitrary BM25 scores.

## References

- [SmartCN plugin](https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-smartcn)
- [ICU tokenizer](https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-icu-tokenizer)
- [ICU analyzer](https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-icu-analyzer)
- [IK source](https://github.com/infinilabs/analysis-ik)
- [Verified IK 9.5.5 archive](https://release.infinilabs.com/analysis-ik/stable/elasticsearch-analysis-ik-9.5.5.zip)
- [CJK bigram filter](https://www.elastic.co/docs/reference/text-analysis/analysis-cjk-bigram-tokenfilter)
- [Combined fields](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-combined-fields-query)
- [Multi-fields and existing documents](https://www.elastic.co/docs/reference/elasticsearch/mapping-reference/multi-fields)
- [External versions](https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-index)
- [Reindex](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/reindex-indices)
