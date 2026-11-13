package nuget

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

func TestParseNuGetConfig(t *testing.T) {
	const feed = "https://feed.example.test/api/nuget/v3/repo"
	cases := []struct {
		name      string
		sources   string
		want      []ecosystem.Declaration
		wantNamed bool
		// after follows </packageSources>, inside <configuration>.
		after string
	}{
		{
			name:      "service index directory is the declared base",
			sources:   `<add key="a" value="` + feed + `/index.json" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:      "query and fragment are cleared",
			sources:   `<add key="a" value="` + feed + `/index.json?x=1#frag" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:      "root-level index.json declares the bare origin",
			sources:   `<add key="a" value="https://feed.example.test/index.json" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: "https://feed.example.test"}},
			wantNamed: true,
		},
		{
			name:      "upper-case .JSON still marks a v3 index",
			sources:   `<add key="a" value="` + feed + `/INDEX.JSON" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:      "protocolVersion 3 without .json: the URL is the service index",
			sources:   `<add key="a" value="` + feed + `/?q=1#f" protocolVersion="3" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:      "protocolVersion 2 with a non-json URL is v2",
			sources:   `<add key="a" value="` + feed + `" protocolVersion="2" />`,
			wantNamed: true,
		},
		{
			name:      "v2 source is skipped but counts as named",
			sources:   `<add key="a" value="https://feed.example.test/nuget" />`,
			wantNamed: true,
		},
		{
			name:      "userinfo source is skipped",
			sources:   `<add key="a" value="https://user:pw@feed.example.test/v3/index.json" />`,
			wantNamed: true,
		},
		{
			name:      "local path source is skipped",
			sources:   `<add key="a" value="./local-feed/index.json" />`,
			wantNamed: true,
		},
		{
			name:      "non-http scheme is skipped",
			sources:   `<add key="a" value="file:///srv/feed/index.json" />`,
			wantNamed: true,
		},
		{
			name:    "nuget.org alone is declared Public and never named",
			sources: `<add key="nuget.org" value="https://api.nuget.org/v3/index.json" />`,
			want:    []ecosystem.Declaration{{Host: "api.nuget.org", UpstreamBaseURL: "https://api.nuget.org/v3", Public: true}},
		},
		{
			name: "a feed plus nuget.org names only the feed",
			sources: `<add key="nuget.org" value="https://api.nuget.org/v3/index.json" />
				<add key="a" value="` + feed + `/index.json" />`,
			want: []ecosystem.Declaration{
				{Host: "api.nuget.org", UpstreamBaseURL: "https://api.nuget.org/v3", Public: true},
				{Host: "feed.example.test", UpstreamBaseURL: feed},
			},
			wantNamed: true,
		},
		{
			name: "clear drops an earlier add",
			sources: `<add key="a" value="` + feed + `/index.json" />
				<clear />`,
		},
		{
			name: "add after clear survives",
			sources: `<add key="old" value="https://old.example.test/v3/index.json" />
				<clear />
				<add key="a" value="` + feed + `/index.json" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name: "remove drops a source by key, case-insensitively",
			sources: `<add key="Keep" value="` + feed + `/index.json" />
				<add key="Drop" value="https://drop.example.test/v3/index.json" />
				<remove key="drop" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name: "remove before add does not drop the later add",
			sources: `<remove key="a" />
				<add key="a" value="` + feed + `/index.json" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name: "a later add with the same key replaces the earlier one",
			sources: `<add key="a" value="https://first.example.test/v3/index.json" />
				<add key="A" value="` + feed + `/index.json" />`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name: "a disabled source is neither declared nor named",
			sources: `<add key="Keep" value="` + feed + `/index.json" />
				<add key="Off" value="https://off.example.test/v3/index.json" />`,
			after:     `<disabledPackageSources><add key="off" value="TRUE" /></disabledPackageSources>`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:      "value false leaves a source enabled",
			sources:   `<add key="a" value="` + feed + `/index.json" />`,
			after:     `<disabledPackageSources><add key="a" value="false" /></disabledPackageSources>`,
			want:      []ecosystem.Declaration{{Host: "feed.example.test", UpstreamBaseURL: feed}},
			wantNamed: true,
		},
		{
			name:    "disabling the only source leaves nothing named",
			sources: `<add key="a" value="` + feed + `/index.json" />`,
			after:   `<disabledPackageSources><add key="a" value="true" /></disabledPackageSources>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "<configuration><packageSources>" + tc.sources + "</packageSources>" + tc.after + "</configuration>"
			got, named, err := parseNuGetConfig(content)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) || named != tc.wantNamed {
				t.Errorf("parseNuGetConfig() = %+v, %v; want %+v, %v", got, named, tc.want, tc.wantNamed)
			}
		})
	}
}

func TestParseNuGetConfig_DeclaredEncoding(t *testing.T) {
	for _, enc := range []string{"utf-8", "windows-1252", "iso-8859-1"} {
		t.Run(enc, func(t *testing.T) {
			content := `<?xml version="1.0" encoding="` + enc + `"?><configuration><packageSources>` +
				`<add key="a" value="https://feed.example.test/v3/index.json" /></packageSources></configuration>`
			got, _, err := parseNuGetConfig(content)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Errorf("parseNuGetConfig() = %+v, want one declaration", got)
			}
		})
	}
}

func TestParseNuGetConfig_MalformedXML(t *testing.T) {
	if _, _, err := parseNuGetConfig("<configuration><packageSources>"); err == nil {
		t.Error("parseNuGetConfig() error = nil, want a decode error")
	}
}

func TestRewriteNuGetConfig(t *testing.T) {
	const local = "http://127.0.0.1:27182/feed"
	const routed = local + "/api/nuget/v3/index.json"
	const nugetOrg = "https://api.nuget.org/v3/index.json"
	wrap := func(sources, rest string) string {
		return "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n" +
			sources + "  </packageSources>\n" + rest + "</configuration>\n"
	}
	cases := []struct {
		name, in, want string
	}{
		{
			name: "self-closing with a space keeps it",
			in:   wrap(`    <add key="a" value="`+routed+`" />`+"\n", ""),
			want: wrap(`    <add key="a" value="`+routed+`" allowInsecureConnections="true" />`+"\n", ""),
		},
		{
			name: "self-closing without a space keeps that",
			in:   wrap(`    <add key="a" value="`+routed+`"/>`+"\n", ""),
			want: wrap(`    <add key="a" value="`+routed+`" allowInsecureConnections="true"/>`+"\n", ""),
		},
		{
			name: "non-self-closing add",
			in:   wrap(`    <add key="a" value="`+routed+`"></add>`+"\n", ""),
			want: wrap(`    <add key="a" value="`+routed+`" allowInsecureConnections="true"></add>`+"\n", ""),
		},
		{
			name: "an existing false is set to true",
			in:   wrap(`    <add key="a" value="`+routed+`" allowInsecureConnections="false" protocolVersion="3" />`+"\n", ""),
			want: wrap(`    <add key="a" value="`+routed+`" allowInsecureConnections="true" protocolVersion="3" />`+"\n", ""),
		},
		{
			name: "an existing true is left byte-identical",
			in:   wrap(`    <add key="a" value='`+routed+`' allowInsecureConnections = 'true' />`+"\n", ""),
			want: wrap(`    <add key="a" value='`+routed+`' allowInsecureConnections = 'true' />`+"\n", ""),
		},
		{
			name: "only the routed source is marked, beside nuget.org and clear",
			in: wrap("    <clear />\n    <add key=\"a\" value=\""+routed+"\" />\n    <add key=\"nuget.org\" value=\""+nugetOrg+"\" />\n",
				"  <packageSourceMapping>\n    <packageSource key=\"a\">\n      <package pattern=\"*\" />\n    </packageSource>\n  </packageSourceMapping>\n"),
			want: wrap("    <clear />\n    <add key=\"a\" value=\""+routed+"\" allowInsecureConnections=\"true\" />\n    <add key=\"nuget.org\" value=\""+nugetOrg+"\" />\n",
				"  <packageSourceMapping>\n    <packageSource key=\"a\">\n      <package pattern=\"*\" />\n    </packageSource>\n  </packageSourceMapping>\n"),
		},
		{
			name: "a look-alike prefix is not routed",
			in:   wrap(`    <add key="a" value="http://127.0.0.1:271820/feed/index.json" />`+"\n", ""),
			want: wrap(`    <add key="a" value="http://127.0.0.1:271820/feed/index.json" />`+"\n", ""),
		},
		{
			name: "credentials are dropped for the routed key only",
			in: wrap("    <add key=\"a\" value=\""+routed+"\" />\n    <add key=\"b\" value=\"https://other.example.test/index.json\" />\n",
				"  <packageSourceCredentials>\n    <a>\n      <add key=\"Username\" value=\"u\" />\n    </a>\n    <b>\n      <add key=\"Username\" value=\"v\" />\n    </b>\n  </packageSourceCredentials>\n"),
			want: wrap("    <add key=\"a\" value=\""+routed+"\" allowInsecureConnections=\"true\" />\n    <add key=\"b\" value=\"https://other.example.test/index.json\" />\n",
				"  <packageSourceCredentials>\n    <b>\n      <add key=\"Username\" value=\"v\" />\n    </b>\n  </packageSourceCredentials>\n"),
		},
		{
			name: "an encoded, differently-cased element name matches its key",
			in: wrap("    <add key=\"My Feed\" value=\""+routed+"\" />\n",
				"  <packageSourceCredentials>\n    <my_x0020_FEED>\n      <add key=\"Username\" value=\"u\" />\n    </my_x0020_FEED>\n  </packageSourceCredentials>\n"),
			want: wrap("    <add key=\"My Feed\" value=\""+routed+"\" allowInsecureConnections=\"true\" />\n",
				"  <packageSourceCredentials>\n  </packageSourceCredentials>\n"),
		},
		{
			name: "credentials declared before the sources are still dropped",
			in:   "<configuration>\n  <packageSourceCredentials>\n    <a><add key=\"Username\" value=\"u\" /></a>\n  </packageSourceCredentials>\n  <packageSources>\n    <add key=\"a\" value=\"" + routed + "\" />\n  </packageSources>\n</configuration>\n",
			want: "<configuration>\n  <packageSourceCredentials>\n  </packageSourceCredentials>\n  <packageSources>\n    <add key=\"a\" value=\"" + routed + "\" allowInsecureConnections=\"true\" />\n  </packageSources>\n</configuration>\n",
		},
		{
			name: "CRLF line endings leave no stray line behind",
			in: "<configuration>\r\n  <packageSources>\r\n    <add key=\"a\" value=\"" + routed + "\" />\r\n  </packageSources>\r\n" +
				"  <packageSourceCredentials>\r\n    <a>\r\n      <add key=\"Username\" value=\"u\" />\r\n    </a>\r\n  </packageSourceCredentials>\r\n</configuration>\r\n",
			want: "<configuration>\r\n  <packageSources>\r\n    <add key=\"a\" value=\"" + routed + "\" allowInsecureConnections=\"true\" />\r\n  </packageSources>\r\n" +
				"  <packageSourceCredentials>\r\n  </packageSourceCredentials>\r\n</configuration>\r\n",
		},
		{
			name: "nothing routed is byte-identical, credentials included",
			in: wrap("    <add key=\"b\" value=\"https://other.example.test/index.json\" />\n",
				"  <packageSourceCredentials>\n    <b>\n      <add key=\"Username\" value=\"v\" />\n    </b>\n  </packageSourceCredentials>\n"),
			want: wrap("    <add key=\"b\" value=\"https://other.example.test/index.json\" />\n",
				"  <packageSourceCredentials>\n    <b>\n      <add key=\"Username\" value=\"v\" />\n    </b>\n  </packageSourceCredentials>\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rewriteNuGetConfig(tc.in, []string{local})
			if err != nil {
				t.Fatalf("rewriteNuGetConfig: %v", err)
			}
			if got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestRewriteNuGetConfig_MalformedXML(t *testing.T) {
	if _, err := rewriteNuGetConfig("<configuration><packageSources>", []string{"http://127.0.0.1:1/x"}); err == nil {
		t.Error("err = nil, want a decode error")
	}
}
