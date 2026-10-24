package npmfamily

import (
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// A registry declared both scoped and unscoped is one Declaration, and it is
// unscoped whichever came first: the unscoped declaration is the default
// registry, so a scoped one must not hide it from the binding. Also covers
// scoped-only/unscoped-only parsing and a yarn column-0 line closing npmScopes.
func TestParseNpmFamilyRegistryConfig_Scoped(t *testing.T) {
	const a, b = "https://a.example.com", "https://b.example.com"
	tests := []struct {
		name, row, content string
		want               []ecosystem.Declaration
	}{
		{"npm scoped only", "npm", "@org:registry=" + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a, Scoped: true}}},
		{"npm unscoped only", "npm", "registry=" + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"npm scoped then unscoped, same URL", "npm", "@org:registry=" + a + "\nregistry=" + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"npm unscoped then scoped, same URL", "npm", "registry=" + a + "\n@org:registry=" + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"npm scoped and unscoped, distinct URLs", "npm", "@org:registry=" + a + "\nregistry=" + b + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a, Scoped: true}, {Host: "b.example.com", UpstreamBaseURL: b}}},

		{"pnpm scoped only", "pnpm", "catalog:\n  \"@org:registry\": " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a, Scoped: true}}},
		{"pnpm unscoped only", "pnpm", "registry: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"pnpm scoped then unscoped, same URL", "pnpm", "catalog:\n  \"@org:registry\": " + a + "\nregistry: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"pnpm unscoped then scoped, same URL", "pnpm", "registry: " + a + "\ncatalog:\n  \"@org:registry\": " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},

		{"yarn scoped only", "yarn", "npmScopes:\n  org:\n    npmRegistryServer: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a, Scoped: true}}},
		{"yarn unscoped only", "yarn", "npmRegistryServer: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"yarn scoped then unscoped, same URL", "yarn", "npmScopes:\n  org:\n    npmRegistryServer: " + a + "\nnpmRegistryServer: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"yarn unscoped then scoped, same URL", "yarn", "npmRegistryServer: " + a + "\nnpmScopes:\n  org:\n    npmRegistryServer: " + a + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a}}},
		{"yarn top-level key after npmScopes block is unscoped", "yarn", "npmScopes:\n  org:\n    npmRegistryServer: " + a + "\nnpmRegistryServer: " + b + "\n",
			[]ecosystem.Declaration{{Host: "a.example.com", UpstreamBaseURL: a, Scoped: true}, {Host: "b.example.com", UpstreamBaseURL: b}}},
	}
	rows := map[string]ecosystem.Strategy{"npm": Npm, "pnpm": Pnpm, "yarn": Yarn}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decls, _, err := rows[tc.row].ConfigParser(tc.content)
			if err != nil {
				t.Fatalf("ConfigParser: unexpected error: %v", err)
			}
			if len(decls) != len(tc.want) {
				t.Fatalf("decls = %+v, want %+v", decls, tc.want)
			}
			for i, w := range tc.want {
				if decls[i] != w {
					t.Errorf("decls[%d] = %+v, want %+v", i, decls[i], w)
				}
			}
		})
	}
}
