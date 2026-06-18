package main

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"text/template"
)

//go:embed templates/k8s/*.yaml.tmpl templates/k8s/testdata/*.yaml.tmpl
var k8sTemplateFS embed.FS

func renderK8STemplate(name string, data any) (string, error) {
	tplPath := "templates/k8s/" + name
	tpl, err := template.New(name).Option("missingkey=error").ParseFS(k8sTemplateFS, tplPath)
	if err != nil {
		return "", fmt.Errorf("parse Kubernetes template %s: %w", name, err)
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, path.Base(tplPath), data); err != nil {
		return "", fmt.Errorf("render Kubernetes template %s: %w", name, err)
	}
	return out.String(), nil
}

func renderK8SNamespaceManifest(name, stack string) (string, error) {
	return renderK8STemplate("namespace.yaml.tmpl", map[string]string{
		"Name":  name,
		"Stack": stack,
	})
}
