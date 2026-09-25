package main

import (
	"encoding/json"
	"strings"
)

// Managed environments remain private previews until indexing is explicitly
// enabled. Enforce at the TLS ingress, including non-HTML responses.
const lkeNoIndexHeader = `more_set_headers "X-Robots-Tag: noindex, nofollow, noarchive, nosnippet, noimageindex";`

const lkeNoIndexServerSnippet = lkeNoIndexHeader + `
location = /robots.txt {
    default_type text/plain;
    return 200 "User-agent: *\nUser-agent: AdsBot-Google\nUser-agent: AdsBot-Google-Mobile\nDisallow: /\n";
}
location ~* ^/(?:sitemap[^/]*\.xml(?:\.gz)?|sites\.xml)$ {
    return 404;
}
`

func lkeIngressNoIndexHelmValue(env map[string]string) string {
	// JSON preserves commas and escaped newlines through Helm's argument parser.
	config := map[string]string{
		"allow-snippet-annotations": "true",
		"annotations-risk-level":    "Critical",
		"server-snippet":            "",
		"location-snippet":          "",
	}
	if lkeDisableSearchIndexing(env) {
		config["server-snippet"] = lkeNoIndexServerSnippet
		config["location-snippet"] = lkeNoIndexHeader
	}
	value, _ := json.Marshal(config)
	return "controller.config=" + string(value)
}

func lkeDisableSearchIndexing(env map[string]string) bool {
	return lkeEnvValue(env, "DISABLE_SEARCH_INDEXING") != "false"
}

func lkePrivateIngressAnnotations(env map[string]string, existing string) string {
	if lkeDisableSearchIndexing(env) {
		return existing
	}
	serverSnippet := "    nginx.ingress.kubernetes.io/server-snippet: |\n      " +
		strings.ReplaceAll(strings.TrimSuffix(lkeNoIndexServerSnippet, "\n"), "\n", "\n      ") + "\n"
	const locationSnippet = "    nginx.ingress.kubernetes.io/configuration-snippet: |\n"
	if strings.Contains(existing, locationSnippet) {
		existing = strings.Replace(existing, locationSnippet, locationSnippet+"      "+lkeNoIndexHeader+"\n", 1)
	} else {
		existing += locationSnippet + "      " + lkeNoIndexHeader + "\n"
	}
	return serverSnippet + existing
}
