package main

type k8sMetadata struct {
	Labels map[string]string
}

func newK8SMetadata(stack, app string) k8sMetadata {
	return k8sMetadata{Labels: map[string]string{
		"app.kubernetes.io/name":    app,
		"app.kubernetes.io/part-of": stack,
		"rtk.realtek.com/stack":     stack,
		"rtk.realtek.com/runtime":   "kubernetes",
	}}
}
