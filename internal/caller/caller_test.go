package caller_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/caller"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func hashOf(bearer string) string {
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:])
}

const (
	pilotBearer   = "pilot-bearer-0123456789abcdef0123456789abcdef"
	stagingBearer = "staging-bearer-0123456789abcdef0123456789abcde"
	rotatedBearer = "rotated-bearer-0123456789abcdef0123456789abcde"
)

func twoClients() string {
	return "clients:\n" +
		"  - name: fuseone-pilot\n" +
		"    token_sha256: " + hashOf(pilotBearer) + "\n" +
		"    repositories: [acme/payments]\n" +
		"    rotation_owner: platform-team\n" +
		"  - name: fuseone-staging\n" +
		"    token_sha256: " + strings.ToUpper(hashOf(stagingBearer)) + "\n" +
		"    repositories: [acme/*, acme/ledger]\n" +
		"    rotation_owner: platform-team\n"
}

func mustParse(t *testing.T, src string) caller.Registry {
	t.Helper()
	reg, err := caller.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return reg
}

func TestAuthenticate_bearerOfAnyEntry_namesThatEntrysClient(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	tests := []struct{ bearer, want string }{
		{bearer: pilotBearer, want: "fuseone-pilot"},
		{bearer: stagingBearer, want: "fuseone-staging"},
	}
	for _, tt := range tests {
		got, err := reg.Authenticate(tt.bearer)
		if err != nil {
			t.Fatalf("Authenticate(%s's bearer): %v", tt.want, err)
		}
		if got.Name != tt.want {
			t.Fatalf("Authenticate = %q, want %q", got.Name, tt.want)
		}
	}
}

func TestAuthenticate_unknownBearer_errUnknownBearer(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	for _, bearer := range []string{"", "not-a-bearer", hashOf(pilotBearer), pilotBearer + " ", strings.ToUpper(pilotBearer)} {
		if _, err := reg.Authenticate(bearer); !errors.Is(err, caller.ErrUnknownBearer) {
			t.Fatalf("Authenticate(%q) err = %v, want caller.ErrUnknownBearer", bearer, err)
		}
	}
}

func TestAuthenticate_zeroRegistry_authenticatesNoOne(t *testing.T) {
	t.Parallel()
	if _, err := (caller.Registry{}).Authenticate(""); !errors.Is(err, caller.ErrUnknownBearer) {
		t.Fatalf("zero Registry err = %v, want caller.ErrUnknownBearer", err)
	}
}

func TestAuthenticate_rotation_bothBearersAreTheSameClient(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients()+
		"  - name: fuseone-pilot\n"+
		"    token_sha256: "+hashOf(rotatedBearer)+"\n"+
		"    repositories: [acme/payments]\n"+
		"    rotation_owner: platform-team\n")
	for _, bearer := range []string{pilotBearer, rotatedBearer} {
		got, err := reg.Authenticate(bearer)
		if err != nil || got.Name != "fuseone-pilot" {
			t.Fatalf("Authenticate = (%q, %v), want fuseone-pilot", got.Name, err)
		}
	}
}

func TestParse_fullEntry_keepsRepositoriesAndRotationOwner(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	got, err := reg.Authenticate(stagingBearer)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if strings.Join(got.Repositories, ",") != "acme/*,acme/ledger" || got.RotationOwner != "platform-team" {
		t.Fatalf("client = %+v, want repositories [acme/* acme/ledger] and rotation owner platform-team", got)
	}
}

func TestParse_noClients_errNoClients(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"", "\n", "clients: []\n", "clients:\n", "# nothing\n"} {
		if _, err := caller.Parse([]byte(src)); !errors.Is(err, caller.ErrNoClients) {
			t.Fatalf("Parse(%q) err = %v, want ErrNoClients", src, err)
		}
	}
}

func yamlEntry(name, hash, repos, owner string) string {
	src := "clients:\n  - name: " + name + "\n"
	if hash != "" {
		src += "    token_sha256: " + hash + "\n"
	}
	if repos != "" {
		src += "    repositories: " + repos + "\n"
	}
	if owner != "" {
		src += "    rotation_owner: " + owner + "\n"
	}
	return src
}

func TestParse_malformedEntry_errMalformedNamingTheField(t *testing.T) {
	t.Parallel()
	good := hashOf(pilotBearer)
	tests := []struct{ name, src, field string }{
		{name: "no name", src: yamlEntry(`""`, good, "[acme/payments]", "team"), field: "name"},
		{name: "name with a newline", src: yamlEntry(`"pilot\nforjad: call client=admin"`, good, "[acme/payments]", "team"), field: "name"},
		{name: "name is the log's absent marker", src: yamlEntry(`"-"`, good, "[acme/payments]", "team"), field: "name"},
		{name: "second document", src: yamlEntry("pilot", good, "[acme/payments]", "team") + "---\n" + yamlEntry("late", hashOf(rotatedBearer), "[acme/payments]", "team"), field: "document"},
		{name: "name with a space", src: yamlEntry(`"pilot tool=x"`, good, "[acme/payments]", "team"), field: "name"},
		{name: "no hash", src: yamlEntry("pilot", "", "[acme/payments]", "team"), field: "token_sha256"},
		{name: "short hash", src: yamlEntry("pilot", good[:62], "[acme/payments]", "team"), field: "token_sha256"},
		{name: "long hash", src: yamlEntry("pilot", good+"00", "[acme/payments]", "team"), field: "token_sha256"},
		{name: "hash not hex", src: yamlEntry("pilot", "zz"+good[2:], "[acme/payments]", "team"), field: "token_sha256"},
		{name: "no repositories", src: yamlEntry("pilot", good, "", "team"), field: "repositories"},
		{name: "empty repositories", src: yamlEntry("pilot", good, "[]", "team"), field: "repositories"},
		{name: "repository without owner", src: yamlEntry("pilot", good, "[payments]", "team"), field: "repositories"},
		{name: "repository with two slashes", src: yamlEntry("pilot", good, "[acme/pay/ments]", "team"), field: "repositories"},
		{name: "wildcard owner", src: yamlEntry("pilot", good, "['*/payments']", "team"), field: "repositories"},
		{name: "partial wildcard", src: yamlEntry("pilot", good, "[acme/pay*]", "team"), field: "repositories"},
		{name: "space in repository", src: yamlEntry("pilot", good, "['acme/pay ments']", "team"), field: "repositories"},
		{name: "dot owner", src: yamlEntry("pilot", good, "[./payments]", "team"), field: "repositories"},
		{name: "dot-dot owner", src: yamlEntry("pilot", good, "[../payments]", "team"), field: "repositories"},
		{name: "dot-dot owner, wildcard", src: yamlEntry("pilot", good, "['../*']", "team"), field: "repositories"},
		{name: "dot name", src: yamlEntry("pilot", good, "[acme/.]", "team"), field: "repositories"},
		{name: "dot-dot name", src: yamlEntry("pilot", good, "[acme/..]", "team"), field: "repositories"},
		{name: "dot-dot name after a valid one", src: yamlEntry("pilot", good, "[acme/payments, acme/..]", "team"), field: "repositories[1]"},
		{name: "the stdio client's name", src: yamlEntry(domain.StdioClientName, good, "[acme/payments]", "team"), field: "name"},
		{name: "the stdio client's name in another case", src: yamlEntry("STDIO", good, "[acme/payments]", "team"), field: "name"},
		{name: "no rotation owner", src: yamlEntry("pilot", good, "[acme/payments]", ""), field: "rotation_owner"},
		{name: "unknown field", src: yamlEntry("pilot", good, "[acme/payments]", "team") + "    scopes: [all]\n", field: "scopes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := caller.Parse([]byte(tt.src))
			if !errors.Is(err, caller.ErrMalformed) || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Parse err = %v, want ErrMalformed naming %s", err, tt.field)
			}
		})
	}
}

func TestParse_sameHashTwice_errMalformed(t *testing.T) {
	t.Parallel()
	src := twoClients() +
		"  - name: someone-else\n" +
		"    token_sha256: " + hashOf(pilotBearer) + "\n" +
		"    repositories: [acme/payments]\n" +
		"    rotation_owner: platform-team\n"
	if _, err := caller.Parse([]byte(src)); !errors.Is(err, caller.ErrMalformed) {
		t.Fatalf("Parse err = %v, want ErrMalformed for one bearer on two entries", err)
	}
}

func TestParse_rotationEntriesDisagree_errMalformed(t *testing.T) {
	t.Parallel()
	rotated := hashOf(rotatedBearer)
	tests := map[string]string{
		"wider repositories":     yamlEntry("fuseone-pilot", rotated, "[acme/*]", "platform-team"),
		"reordered repositories": yamlEntry("fuseone-pilot", rotated, "[acme/payments, acme/ledger]", "platform-team"),
		"other rotation owner":   yamlEntry("fuseone-pilot", rotated, "[acme/payments]", "security-team"),
	}
	for name, extra := range tests {
		src := twoClients() + extra[len("clients:\n"):]
		_, err := caller.Parse([]byte(src))
		if !errors.Is(err, caller.ErrMalformed) || !strings.Contains(err.Error(), "fuseone-pilot") {
			t.Fatalf("%s: Parse err = %v, want ErrMalformed naming fuseone-pilot", name, err)
		}
	}
}

func TestParse_rotationSameScopeOtherOrder_accepted(t *testing.T) {
	t.Parallel()
	src := yamlEntry("pilot", hashOf(pilotBearer), "[a/x, a/y]", "team") +
		yamlEntry("pilot", hashOf(rotatedBearer), "[a/y, a/x]", "team")[len("clients:\n"):]
	if _, err := caller.Parse([]byte(src)); err != nil {
		t.Fatalf("Parse: %v, want the same scope listed in another order accepted", err)
	}
}

func TestAuthenticate_callerEditsRepositories_registryUnchanged(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	first, err := reg.Authenticate(pilotBearer)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	first.Repositories[0] = "evil/*"
	again, _ := reg.Authenticate(pilotBearer)
	if again.Repositories[0] != "acme/payments" {
		t.Fatalf("repositories = %v after a caller's edit, want [acme/payments]", again.Repositories)
	}
}

func TestParse_malformed_errorCarriesNoHash(t *testing.T) {
	t.Parallel()
	hash := hashOf(pilotBearer)
	tests := map[string]string{
		"hash where a list goes": yamlEntry("pilot", hash, hash, "team"),
		"hash as a key":          "clients:\n  - " + hash + ": x\n",
		"short hash":             yamlEntry("pilot", hash[:63], "[acme/payments]", "team"),
		"duplicate hash":         twoClients() + yamlEntry("again", hash, "[acme/payments]", "team")[len("clients:\n"):],
	}
	for name, src := range tests {
		_, err := caller.Parse([]byte(src))
		if err == nil {
			t.Fatalf("%s: Parse succeeded", name)
		}
		if strings.Contains(strings.ToLower(err.Error()), hash[:16]) {
			t.Fatalf("%s: error %q carries the hash", name, err)
		}
	}
}

func TestParse_validRepositoryPattern_accepted(t *testing.T) {
	t.Parallel()
	for _, repo := range []string{"acme/payments", "acme/*", "Acme-Co/pay.ments_v2", "a/b"} {
		if _, err := caller.Parse([]byte(yamlEntry("pilot", hashOf(pilotBearer), "['"+repo+"']", "team"))); err != nil {
			t.Fatalf("repository %q rejected: %v", repo, err)
		}
	}
}

// Every bearer authenticates as the one entry whose hash is its own, and
// no bearer authenticates as an entry it does not hash to.
func TestAuthenticate_property_onlyTheOwnBearerMatches(t *testing.T) {
	t.Parallel()
	prop := func(a, b string) bool {
		if a == b {
			return true
		}
		reg, err := caller.Parse([]byte(yamlEntry("a", hashOf(a), "[o/r]", "t") + yamlEntry("b", hashOf(b), "[o/r]", "t")[len("clients:\n"):]))
		if err != nil {
			return false
		}
		gotA, errA := reg.Authenticate(a)
		gotB, errB := reg.Authenticate(b)
		_, errC := reg.Authenticate(a + b + "x")
		return errA == nil && gotA.Name == "a" && errB == nil && gotB.Name == "b" && errors.Is(errC, caller.ErrUnknownBearer)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLookup_knownName_clientWithItsRepositoriesOnly(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	got, ok := reg.Lookup("fuseone-staging")
	if !ok || got.Name != "fuseone-staging" || got.Unrestricted || strings.Join(got.Repositories, ",") != "acme/*,acme/ledger" {
		t.Fatalf("Lookup = (%+v, %v), want fuseone-staging restricted to [acme/* acme/ledger]", got, ok)
	}
	if !got.Reaches("acme/anything") || got.Reaches("evil/anything") {
		t.Fatalf("client %+v reaches the wrong repositories", got)
	}
}

func TestLookup_unknownName_noClient(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	for _, name := range []string{"", "fuseone", "FUSEONE-PILOT", domain.StdioClientName, "-"} {
		if got, ok := reg.Lookup(name); ok || got.Name != "" || got.Reaches("acme/payments") {
			t.Fatalf("Lookup(%q) = (%+v, %v), want no client", name, got, ok)
		}
	}
	if _, ok := (caller.Registry{}).Lookup("fuseone-pilot"); ok {
		t.Fatal("the zero Registry looked up a client")
	}
}

func TestLookup_callerEditsRepositories_registryUnchanged(t *testing.T) {
	t.Parallel()
	reg := mustParse(t, twoClients())
	first, _ := reg.Lookup("fuseone-pilot")
	first.Repositories[0] = "evil/*"
	again, _ := reg.Lookup("fuseone-pilot")
	if again.Reaches("evil/x") {
		t.Fatalf("repositories = %v after a caller's edit, want [acme/payments]", again.Repositories)
	}
}
