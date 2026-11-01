#!/usr/bin/env python3
"""Disposable ES analyzer/token and native relevance study; no app dependencies."""
import argparse
import datetime
import json
from pathlib import Path
import urllib.error
import urllib.request

ANALYZERS = {"standard": "standard", "cjk": "cjk", "smartcn": "smartcn", "icu": "icu_analyzer", "smartcn_nostem": "smartcn_nostem", "ik_smart": "ik_smart", "ik_max_word": "ik_max_word"}
DOCS = [
    ("toothpaste", "", "\u7259\u818f\u6211\u53ea\u9009\u4e2d\u534e\u4e3a\u4ec0\u4e48\uff1f"),
    ("huawei", "", "\u534e\u4e3a"),
    ("huawei_laptop", "", "\u534e\u4e3a\u7b14\u8bb0\u672c"),
    ("apple_laptop", "", "\u82f9\u679c\u7b14\u8bb0\u672c"),
    ("database_cn", "", "\u6570\u636e\u5e93"),
    ("data_cn", "", "\u6570\u636e"),
    ("single_ben", "", "\u672c"),
    ("transport", "", "transport"),
    ("port_underscore", "", "port_name"),
    ("port_hyphen", "", "port-name"),
    ("port_case", "", "Port"),
    ("port", "", "port"),
    ("ports", "", "ports"),
    ("cpp", "", "C++"),
    ("c", "", "C"),
    ("db", "", "db"),
    ("database_en", "", "database databases"),
    ("stems", "", "running runs runner databases database relational"),
    ("mixed", "", "\u6211\u7528 PostgreSQL \u6570\u636e\u5e93\uff0c\u901a\u8fc7 HTTP API \u67e5\u8be2 db\uff0c\u8fd4\u56de JSON\u3002"),
    ("protocol", "", "pax API api_key session_id C++ TCP port_name transport database DB"),
    ("cross_fields", "port", "tcp database"),
    ("same_field", "", "port tcp database"),
    ("partial_fields", "port", "tcp"),
    ("chinese_fields", "\u534e\u4e3a \u6570\u636e\u5e93 API", "\u7b14\u8bb0\u672c port 9879"),
]
QUERIES = ["\u534e\u4e3a", "\u6570\u636e", "\u6570\u636e\u5e93", "\u7b14\u8bb0\u672c", "\u672c", "\u534e\u4e3a\u7b14\u8bb0\u672c", "port", "ports", "C++", "C", "db", "API", "\u4e2d\u6587 API", "port tcp database", "C++ C", "port port:", "\u6570\u636e \u5e93", "running", "runs", "databases", "database"]
PAIRS = [("\u534e\u4e3a", "toothpaste"), ("\u6570\u636e", "database_cn"), ("\u7b14\u8bb0\u672c", "apple_laptop"), ("\u672c", "apple_laptop"), ("\u534e\u4e3a\u7b14\u8bb0\u672c", "apple_laptop"), ("port", "transport"), ("port", "port_underscore"), ("port", "port_hyphen"), ("port", "port_case"), ("C++", "cpp"), ("C++", "c"), ("db", "database_cn"), ("port tcp database", "cross_fields"), ("ports", "port"), ("databases", "database_en")]


def dump(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--index", default="pax-analyzer-study-toy")
    parser.add_argument("--out", default="results")
    args = parser.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    def request(method, path, data=None, ndjson=False):
        payload = data.encode() if ndjson else (None if data is None else json.dumps(data, ensure_ascii=False).encode())
        req = urllib.request.Request(args.url.rstrip("/") + path, data=payload, method=method,
                                     headers={"Content-Type": "application/x-ndjson" if ndjson else "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f"{method} {path}: HTTP {exc.code}: {exc.read().decode()}") from exc

    dump(out / "version.json", request("GET", "/"))
    dump(out / "plugins.json", request("GET", "/_nodes/plugins"))
    text_fields = {key: {"type": "text", "analyzer": analyzer} for key, analyzer in ANALYZERS.items() if key != "standard"}
    text_fields["raw"] = {"type": "wildcard"}
    mapping = {"settings": {"number_of_shards": 1, "number_of_replicas": 0,
                 "analysis": {"analyzer": {"smartcn_nostem": {"type": "custom", "tokenizer": "smartcn_tokenizer", "filter": ["lowercase", "smartcn_stop"]}}}},
               "mappings": {"properties": {"doc_id": {"type": "keyword"}, "owner": {"type": "keyword"},
                 "title": {"type": "text", "analyzer": "standard", "fields": text_fields},
                 "body": {"type": "text", "analyzer": "standard", "fields": text_fields}}}}
    dump(out / "create_index_request.json", mapping)
    request("PUT", "/" + args.index, mapping)
    bulk = ""
    for doc_id, title, body in DOCS:
        bulk += json.dumps({"index": {"_index": args.index, "_id": doc_id}}) + "\n"
        bulk += json.dumps({"doc_id": doc_id, "owner": "research", "title": title, "body": body}, ensure_ascii=False) + "\n"
    response = request("POST", "/_bulk?refresh=true", bulk, ndjson=True)
    assert not response.get("errors"), response
    foreign = {"doc_id": "foreign", "owner": "other", "title": "\u534e\u4e3a port", "body": "\u534e\u4e3a\u7b14\u8bb0\u672c \u6570\u636e\u5e93 port tcp database C++"}
    request("PUT", "/" + args.index + "/_doc/foreign?refresh=true", foreign)
    dump(out / "corpus.json", [{"id": doc_id, "owner": "research", "title": title, "body": body} for doc_id, title, body in DOCS] + [{"id": "foreign", **foreign}])

    texts = list(dict.fromkeys([body for _, _, body in DOCS] + [title for _, title, _ in DOCS if title] + QUERIES))
    analyses = []
    token_lines = ["analyzer\ttext\ttoken\tposition\tstart_offset\tend_offset\ttype"]
    for label, analyzer in ANALYZERS.items():
        for text in texts:
            response = request("POST", "/" + args.index + "/_analyze", {"analyzer": analyzer, "text": text})
            analyses.append({"analyzer": label, "text": text, "request": {"analyzer": analyzer, "text": text}, "response": response})
            for token in response["tokens"]:
                token_lines.append("\t".join(str(v) for v in [label, text, token["token"], token["position"], token["start_offset"], token["end_offset"], token["type"]]))
    dump(out / "analyze.json", analyses)
    (out / "tokens.tsv").write_text("\n".join(token_lines) + "\n")
    explains = {}
    for label, analyzer in ANALYZERS.items():
        explains[label] = request("POST", "/" + args.index + "/_analyze", {"analyzer": analyzer, "text": "running runs databases ports C++ Port port_name", "explain": True})
    dump(out / "analyze_explain.json", explains)

    def fields(label):
        return ["title", "body"] if label == "standard" else ["title." + label, "body." + label]

    def raw(query, operator):
        terms = list(dict.fromkeys(query.lower().split()))
        clauses = []
        for term in terms:
            literal = term.replace("\\", "\\\\").replace("*", "\\*").replace("?", "\\?")
            clauses.append({"bool": {"should": [{"wildcard": {field + ".raw": {"value": "*" + literal + "*", "case_insensitive": True}}} for field in ["title", "body"]], "minimum_should_match": 1}})
        return {"bool": {"must": clauses}} if operator == "and" else {"bool": {"should": clauses, "minimum_should_match": 1}}

    searches = []
    hit_sets = {}
    lines = ["analyzer\tshape\tquery\thits_with_scores"]
    for query in QUERIES:
        strategies = {"raw_any": raw(query, "or"), "raw_all": raw(query, "and")}
        for label in ANALYZERS:
            fs = fields(label)
            strategies.update({label + ":most_fields_or": {"multi_match": {"query": query, "fields": fs, "type": "most_fields", "operator": "or"}},
                               label + ":most_fields_and": {"multi_match": {"query": query, "fields": fs, "type": "most_fields", "operator": "and"}},
                               label + ":combined_fields_or": {"combined_fields": {"query": query, "fields": fs, "operator": "or"}},
                               label + ":combined_fields_and": {"combined_fields": {"query": query, "fields": fs, "operator": "and"}},
                               label + ":dis_max_or": {"dis_max": {"queries": [{"match": {field: {"query": query, "operator": "or"}}} for field in fs], "tie_breaker": 0}},
                               label + ":phrase": {"bool": {"should": [{"match_phrase": {field: query}} for field in fs], "minimum_should_match": 1}},
                               label + ":fulltext_with_low_raw_fallback": {"dis_max": {"queries": [{"combined_fields": {"query": query, "fields": fs, "operator": "or"}}, {"constant_score": {"filter": raw(query, "or"), "boost": 0.05}}], "tie_breaker": 0}}})
        strategies["ik_max_search_smart:cross_fields_and"] = {"multi_match": {"query": query, "fields": fields("ik_max_word"), "type": "cross_fields", "analyzer": "ik_smart", "operator": "and"}}
        for name, strategy in strategies.items():
            body = {"size": 100, "track_total_hits": True, "query": {"bool": {"filter": {"term": {"owner": "research"}}, "must": strategy}},
                    "sort": [{"_score": "desc"}, {"doc_id": "asc"}]}
            response = request("POST", "/" + args.index + "/_search", body)
            hits = [{"id": hit["_id"], "score": hit["_score"]} for hit in response["hits"]["hits"]]
            assert all(hit["id"] != "foreign" for hit in hits), "owner filter must be mandatory"
            searches.append({"query": query, "strategy": name, "request": body, "response": response})
            hit_sets[(query, name)] = {hit["id"] for hit in hits}
            label, _, shape = name.partition(":")
            lines.append("\t".join([label, shape or name, query, json.dumps(hits, ensure_ascii=False)]))
    dump(out / "search_results.json", searches)
    (out / "matches.tsv").write_text("\n".join(lines) + "\n")
    pair_results = []
    for query, doc_id in PAIRS:
        row = {"query": query, "document_id": doc_id, "raw_any": doc_id in hit_sets[(query, "raw_any")], "raw_all": doc_id in hit_sets[(query, "raw_all")]}
        for label in ANALYZERS:
            row[label] = {shape: doc_id in hit_sets[(query, label + ":" + shape)] for shape in ["most_fields_or", "most_fields_and", "combined_fields_and", "phrase"]}
        row["ik_max_search_smart"] = {"cross_fields_and": doc_id in hit_sets[(query, "ik_max_search_smart:cross_fields_and")]}
        pair_results.append(row)
    dump(out / "pair_outcomes.json", pair_results)
    mapping_probe = {}
    probe_index = args.index + "-wildcard-primary"
    probe_mapping = {"settings": {"number_of_shards": 1, "number_of_replicas": 0}, "mappings": {"properties": {"owner": {"type": "keyword"}, "title": {"type": "wildcard", "fields": {"tokens": {"type": "text", "analyzer": "smartcn"}}}, "body": {"type": "wildcard", "fields": {"tokens": {"type": "text", "analyzer": "smartcn"}}}}}}
    mapping_probe["request"] = probe_mapping
    try:
        mapping_probe["create_response"] = request("PUT", "/" + probe_index, probe_mapping)
        source = {"owner": "research", "title": "\u534e\u4e3a", "body": "\u7259\u818f\u6211\u53ea\u9009\u4e2d\u534e\u4e3a\u4ec0\u4e48\uff1f"}
        request("PUT", "/" + probe_index + "/_doc/probe?refresh=true", source)
        mapping_probe["get_document"] = request("GET", "/" + probe_index + "/_doc/probe")
        assert mapping_probe["get_document"]["_source"] == source
        for name, clause in {"literal": {"wildcard": {"body": {"value": "*\u534e\u4e3a*"}}}, "token": {"match": {"body.tokens": "\u534e\u4e3a"}}}.items():
            mapping_probe[name] = request("POST", "/" + probe_index + "/_search", {"query": {"bool": {"filter": {"term": {"owner": "research"}}, "must": clause}}})
    except RuntimeError as error:
        mapping_probe["error"] = str(error)
    dump(out / "wildcard_primary_mapping_probe.json", mapping_probe)
    migration = {}
    migration_index = args.index + "-inplace"
    initial_mapping = {"settings": {"number_of_shards": 1, "number_of_replicas": 0}, "mappings": {"properties": {"owner": {"type": "keyword"}, "title": {"type": "wildcard"}, "body": {"type": "wildcard"}}}}
    migration["initial_mapping"] = request("PUT", "/" + migration_index, initial_mapping)
    old_source = {"owner": "research", "title": "API", "body": "port tcp \u6570\u636e\u5e93"}
    request("PUT", "/" + migration_index + "/_doc/old?refresh=true", old_source)
    request("PUT", "/" + migration_index + "/_doc/foreign?refresh=true", {"owner": "other", "title": "API", "body": "port tcp \u6570\u636e\u5e93"})
    add_words = {"properties": {field: {"type": "wildcard", "fields": {"words": {"type": "text", "analyzer": "smartcn"}}} for field in ["title", "body"]}}
    migration["put_mapping_request"] = add_words
    try:
        migration["put_mapping_response"] = request("PUT", "/" + migration_index + "/_mapping", add_words)
        token_query = {"query": {"bool": {"filter": {"term": {"owner": "research"}}, "must": {"match": {"body.words": "port"}}}}}
        raw_query = {"query": {"bool": {"filter": {"term": {"owner": "research"}}, "must": {"wildcard": {"body": {"value": "*port*"}}}}}}
        migration["old_token_hits_before_rewrite"] = request("POST", "/" + migration_index + "/_search", token_query)
        assert migration["old_token_hits_before_rewrite"]["hits"]["total"]["value"] == 0
        request("PUT", "/" + migration_index + "/_doc/new?refresh=true", old_source)
        migration["new_document_indexes_words"] = request("POST", "/" + migration_index + "/_search", token_query)
        assert [h["_id"] for h in migration["new_document_indexes_words"]["hits"]["hits"]] == ["new"]
        migration["raw_before_rewrite"] = request("POST", "/" + migration_index + "/_search", raw_query)
        migration["update_by_query_request"] = {"query": {"term": {"owner": "research"}}}
        migration["update_by_query_response"] = request("POST", "/" + migration_index + "/_update_by_query?conflicts=proceed&refresh=true&requests_per_second=5", migration["update_by_query_request"])
        migration["token_hits_after_rewrite"] = request("POST", "/" + migration_index + "/_search", token_query)
        assert {h["_id"] for h in migration["token_hits_after_rewrite"]["hits"]["hits"]} == {"old", "new"}
        migration["raw_after_rewrite"] = request("POST", "/" + migration_index + "/_search", raw_query)
        assert {h["_id"] for h in migration["raw_before_rewrite"]["hits"]["hits"]} == {h["_id"] for h in migration["raw_after_rewrite"]["hits"]["hits"]}
        migration["old_document_after_rewrite"] = request("GET", "/" + migration_index + "/_doc/old")
        assert migration["old_document_after_rewrite"]["_source"] == old_source
        migration["foreign_document_unchanged"] = request("GET", "/" + migration_index + "/_doc/foreign")
        request("PUT", "/" + migration_index + "/_doc/punctuation?refresh=true", {"owner": "research", "title": "", "body": "!!!"})
        migration["punctuation_words_analyze"] = request("POST", "/" + migration_index + "/_analyze", {"field": "body.words", "text": "!!!"})
        migration["punctuation_exists_query"] = request("POST", "/" + migration_index + "/_search", {"query": {"bool": {"filter": [{"ids": {"values": ["punctuation"]}}, {"exists": {"field": "body.words"}}]}}})
        migration["mapping_after"] = request("GET", "/" + migration_index + "/_mapping")
    except RuntimeError as error:
        migration["error"] = str(error)
    dump(out / "inplace_words_migration_probe.json", migration)
    dump(out / "metadata.json", {"created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "url": args.url, "index": args.index,
          "analyzers": ANALYZERS, "document_count": len(DOCS)+1, "query_count": len(QUERIES), "analyze_calls": len(analyses), "search_calls": len(searches),
          "fallback_boost": 0.05, "note": "Illustrative native BM25 plus low constant fallback, not a strict ranking guarantee or production proposal.",
          "offset_note": "Elasticsearch token offsets are UTF-16 code-unit offsets, not UTF-8 bytes.",
          "official_references": ["https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-smartcn", "https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-icu-analyzer", "https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-combined-fields-query"]})
    print(json.dumps({"index": args.index, "documents": len(DOCS)+1, "analyze_calls": len(analyses), "search_calls": len(searches), "output_dir": str(out.resolve())}))


if __name__ == "__main__":
    main()
