package controller

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

// TestAIHubRBAC_SupersetGate is the WI-6 drift guard for the WI-5 union
// approach: it enforces that the AIHub ClusterRole
// (config/overlays/aihub/rbac/role.yaml) is a superset of the union of every
// ClusterRole and Role rendered by the child model-registry operator's
// config/overlays/odh kustomize overlay. Because WI-5 chose the union approach
// (no escalate/bind verbs), the AIHub role is coupled to the child's RBAC
// contents; a new resource/verb on the MR operator side would otherwise
// silently break AIHub's reconcile at runtime. This test turns that "keep in
// sync" comment into an enforced invariant.
func TestAIHubRBAC_SupersetGate(t *testing.T) {
	// --- 1. Render child overlay and collect union of RBAC rules ---
	childOverlayPath := filepath.Join("..", "..", "config", "overlays", "odh")
	resources, err := kustomize.Render(childOverlayPath, nil)
	if err != nil {
		t.Fatalf("kustomize.Render(%s): %v", childOverlayPath, err)
	}

	childResPerms := map[string]bool{}
	childNonResPerms := map[string]bool{}

	for _, res := range resources {
		kind := res.GetKind()
		if kind != "ClusterRole" && kind != "Role" {
			continue
		}
		rules, err := extractRules(&res)
		if err != nil {
			t.Fatalf("extracting rules from %s %q: %v", kind, res.GetName(), err)
		}
		expandRules(rules, childResPerms, childNonResPerms)
	}

	if len(childResPerms) == 0 {
		t.Fatal("child overlay rendered 0 resource permission tuples — likely a rendering issue")
	}

	// --- 2. Parse the AIHub ClusterRole ---
	aihubRolePath := filepath.Join("..", "..", "config", "overlays", "aihub", "rbac", "role.yaml")
	aihubResPerms := map[string]bool{}
	aihubNonResPerms := map[string]bool{}

	aihubRules, err := parseRoleFile(aihubRolePath)
	if err != nil {
		t.Fatalf("parsing AIHub role %s: %v", aihubRolePath, err)
	}
	expandRules(aihubRules, aihubResPerms, aihubNonResPerms)

	// --- 3. Assert superset ---
	var missingRes []string
	for k := range childResPerms {
		if !aihubResPerms[k] {
			missingRes = append(missingRes, k)
		}
	}
	sort.Strings(missingRes)

	var missingNonRes []string
	for k := range childNonResPerms {
		if !aihubNonResPerms[k] {
			missingNonRes = append(missingNonRes, k)
		}
	}
	sort.Strings(missingNonRes)

	if len(missingRes) > 0 || len(missingNonRes) > 0 {
		t.Errorf("AIHub ClusterRole is NOT a superset of the child union.\n"+
			"Missing resource perms (%d):\n  %s\n"+
			"Missing non-resource perms (%d):\n  %s",
			len(missingRes), strings.Join(missingRes, "\n  "),
			len(missingNonRes), strings.Join(missingNonRes, "\n  "))
	}

	t.Logf("child union: %d resource perms, %d non-resource perms", len(childResPerms), len(childNonResPerms))
	t.Logf("aihub role:  %d resource perms, %d non-resource perms", len(aihubResPerms), len(aihubNonResPerms))

	// --- 4. No wildcards / no escalate / no bind ---
	for _, rule := range aihubRules {
		for _, g := range rule.APIGroups {
			if g == "*" {
				t.Errorf("AIHub ClusterRole contains wildcard apiGroup '*'")
			}
		}
		for _, r := range rule.Resources {
			if r == "*" {
				t.Errorf("AIHub ClusterRole contains wildcard resource '*'")
			}
		}
		for _, v := range rule.Verbs {
			if v == "*" {
				t.Errorf("AIHub ClusterRole contains wildcard verb '*'")
			}
			if v == "escalate" {
				t.Errorf("AIHub ClusterRole contains forbidden verb 'escalate'")
			}
			if v == "bind" {
				t.Errorf("AIHub ClusterRole contains forbidden verb 'bind'")
			}
		}
	}
}

// expandRules expands RBAC rules into sets of permission tuple keys.
func expandRules(rules []rbacv1.PolicyRule, resPerms, nonResPerms map[string]bool) {
	for _, rule := range rules {
		// Non-resource URLs.
		for _, url := range rule.NonResourceURLs {
			for _, verb := range rule.Verbs {
				nonResPerms[fmt.Sprintf("%s|%s", url, verb)] = true
			}
		}
		// Resource permissions: cartesian product of apiGroups × resources × verbs.
		for _, group := range rule.APIGroups {
			for _, res := range rule.Resources {
				for _, verb := range rule.Verbs {
					resPerms[fmt.Sprintf("%s|%s|%s", group, res, verb)] = true
				}
			}
		}
	}
}

// extractRules converts an unstructured ClusterRole/Role to typed rules.
func extractRules(u *unstructured.Unstructured) ([]rbacv1.PolicyRule, error) {
	// Try ClusterRole first; Role has the same .rules path.
	cr := &rbacv1.ClusterRole{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cr); err != nil {
		return nil, err
	}
	return cr.Rules, nil
}

// parseRoleFile reads a YAML file (possibly with leading ---) and returns the
// aggregate rules from all ClusterRole/Role documents found.
func parseRoleFile(path string) ([]rbacv1.PolicyRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var allRules []rbacv1.PolicyRule

	for {
		cr := &rbacv1.ClusterRole{}
		if err := decoder.Decode(cr); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding YAML document: %w", err)
		}
		if cr.Kind == "" {
			continue // empty document
		}
		allRules = append(allRules, cr.Rules...)
	}

	return allRules, nil
}
