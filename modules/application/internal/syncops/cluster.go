package syncops

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
)

var kindAPIVersions = map[string]string{
	"Deployment":  "apps/v1",
	"StatefulSet": "apps/v1",
	"Service":     "v1",
	"Ingress":     "networking.k8s.io/v1",
	"ConfigMap":   "v1",
	"Secret":      "v1",
}

// parseGVK derives the GroupVersionKind of a decoded document.
func parseGVK(doc *unstructured.Unstructured) (schema.GroupVersionKind, error) {
	if doc.GetKind() == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("document %s has no kind", store.DocKeyOf(doc))
	}
	gv, err := schema.ParseGroupVersion(doc.GetAPIVersion())
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("parse apiVersion %q: %w", doc.GetAPIVersion(), err)
	}
	return gv.WithKind(doc.GetKind()), nil
}

// RestMapping resolves the API resource (plural name and scope) of a document
// through the cluster's RESTMapper (design §6: 逐文档 RESTMapper 判 scope).
func RestMapping(cli client.Client, doc *unstructured.Unstructured) (meta.RESTMapping, error) {
	gvk, err := parseGVK(doc)
	if err != nil {
		return meta.RESTMapping{}, err
	}
	mapping, err := cli.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return meta.RESTMapping{}, fmt.Errorf("resolve api resource of %v: %w", gvk, err)
	}
	return *mapping, nil
}

// BlankDoc returns an empty unstructured object of the document's GVK, ready
// for Get/Delete calls.
func BlankDoc(tpl *unstructured.Unstructured) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(tpl.GetAPIVersion())
	obj.SetKind(tpl.GetKind())
	return obj
}

// IdentityTemplates returns one template document per (kind, namespace, name)
// across manifests and anchor, manifests order first: the fetch/apply set.
func IdentityTemplates(manifests, anchor []unstructured.Unstructured) []unstructured.Unstructured {
	seen := map[string]bool{}
	var out []unstructured.Unstructured
	add := func(docs []unstructured.Unstructured) {
		for i := range docs {
			key := store.DocKeyOf(&docs[i])
			if seen[key] {
				continue
			}
			seen[key] = true
			tpl := unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": docs[i].GetAPIVersion(),
				"kind":       docs[i].GetKind(),
			}}
			tpl.SetName(docs[i].GetName())
			tpl.SetNamespace(docs[i].GetNamespace())
			out = append(out, tpl)
		}
	}
	add(manifests)
	add(anchor)
	return out
}

// FetchLiveDocs reads the cluster state of the identity documents; objects
// absent on the cluster are simply missing from the result, other read errors
// abort.
func FetchLiveDocs(ctx context.Context, cli client.Client, templates []unstructured.Unstructured) (map[string]*unstructured.Unstructured, error) {
	live := make(map[string]*unstructured.Unstructured, len(templates))
	for i := range templates {
		tpl := &templates[i]
		obj := BlankDoc(tpl)
		err := cli.Get(ctx, client.ObjectKey{Namespace: tpl.GetNamespace(), Name: tpl.GetName()}, obj)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", store.DocKeyOf(tpl), err)
		}
		live[store.DocKeyOf(tpl)] = obj
	}
	return live, nil
}

// ListManagedByLabel lists the managed-kind documents carrying the app's
// identity label in the config namespace (design §6 sync).
func ListManagedByLabel(ctx context.Context, cli client.Client, app, namespace string) (map[string]*unstructured.Unstructured, error) {
	out := map[string]*unstructured.Unstructured{}
	for kind, apiVersion := range kindAPIVersions {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(apiVersion)
		list.SetKind(kind + "List")
		err := cli.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{store.LabelKiteName: app})
		if err != nil {
			return nil, fmt.Errorf("list %s by label: %w", kind, err)
		}
		for i := range list.Items {
			out[store.DocKeyOf(&list.Items[i])] = &list.Items[i]
		}
	}
	return out, nil
}

// LiveList projects the live map onto the ordered doc set.
func LiveList(live map[string]*unstructured.Unstructured, order []unstructured.Unstructured) []unstructured.Unstructured {
	out := make([]unstructured.Unstructured, 0, len(live))
	for i := range order {
		if d, ok := live[store.DocKeyOf(&order[i])]; ok {
			out = append(out, *d)
		}
	}
	return out
}
