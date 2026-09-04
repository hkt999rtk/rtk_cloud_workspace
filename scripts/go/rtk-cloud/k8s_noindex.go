package main

import "encoding/json"

// All managed environments are private previews until explicitly approved for
// search indexing. Enforce at the TLS ingress, including non-HTML responses.
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

func lkeIngressNoIndexHelmValue() string {
	// JSON preserves commas and escaped newlines through Helm's argument parser.
	value, _ := json.Marshal(map[string]string{
		"allow-snippet-annotations": "true",
		"annotations-risk-level":    "Critical",
		"server-snippet":            lkeNoIndexServerSnippet,
		"location-snippet":          lkeNoIndexHeader,
	})
	return "controller.config=" + string(value)
}
