package main

import (
	"fmt"
	rayv1api "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
)

func createPatch(rayCluster *rayv1api.RayCluster) ([]patchOperation, error) {
	fmt.Printf("creating json patch for RayCluster")

	var patches []patchOperation

	oauthSidecar := corev1.Container{
		Name:  "oauth-sidecar",
		Image: "registry.redhat.io/openshift4/ose-oauth-proxy@sha256:1ea6a01bf3e63cdcf125c6064cbd4a4a270deaf0f157b3eabb78f60556840366",
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: 8080,
			},
		},
	}

	patches = append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/headGroupSpec/template/spec/containers/-",
		Value: oauthSidecar,
	})

	return patches, nil
}
