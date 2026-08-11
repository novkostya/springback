// Package store owns everything springback writes to disk: the account records and the library.
// The layout is SPEC §4's, exactly.
package store

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Account is one row of /accounts/accounts.json.
type Account struct {
	Slug  string `json:"slug"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// KeychainPP encrypts ipatool's local credential file. It is NOT an Apple secret:
	// there is no keyring daemon in a container, so --keychain-passphrase is mandatory
	// (SPEC §3), and the passphrase is generated per account and stored beside the record.
	//
	// It never leaves the box and is never sent to a browser — see Public().
	KeychainPP string    `json:"keychain_pp"`
	AddedAt    time.Time `json:"added_at"`
}

// PublicAccount is the shape the HTTP API returns: the same record with the passphrase removed.
//
// A SEPARATE TYPE RATHER THAN A json:"-" TAG, deliberately. The tag approach means the secret is
// one careless struct literal away from being serialised, and the compiler cannot tell the
// difference. Here, handing the API an Account instead of a PublicAccount does not compile.
type PublicAccount struct {
	Slug    string    `json:"slug"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"added_at"`
}

// Public strips what must never reach the browser.
func (a Account) Public() PublicAccount {
	return PublicAccount{Slug: a.Slug, Email: a.Email, Name: a.Name, AddedAt: a.AddedAt}
}

// Home is the HOME for ipatool calls on this account. Isolation is by HOME, measured (SPEC §3):
// ipatool keeps .ipatool/{account,cookies} under it.
func (a Account) Home(root string) string { return filepath.Join(root, a.Slug) }

// Accounts is the accounts.json store.
type Accounts struct {
	Root string
	mu   sync.Mutex
}

func NewAccounts(root string) *Accounts { return &Accounts{Root: root} }

func (s *Accounts) path() string { return filepath.Join(s.Root, "accounts.json") }

// List returns every account record.
func (s *Accounts) List() ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Accounts) loadLocked() ([]Account, error) {
	b, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var accs []Account
	if err := json.Unmarshal(b, &accs); err != nil {
		return nil, fmt.Errorf("accounts.json: %w", err)
	}
	return accs, nil
}

func (s *Accounts) saveLocked(accs []Account) error {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(accs, "", "  ")
	if err != nil {
		return err
	}
	// Written 0600 and via a temp file. The mode because this file holds every keychain
	// passphrase on the box; the rename because a truncated accounts.json loses the record of
	// every signed-in Apple ID, and the sessions in /accounts/<slug> are then orphaned with no
	// way to name them.
	return writeFileAtomic(s.path(), b, 0o600)
}

// Get returns one account by slug.
func (s *Accounts) Get(slug string) (Account, error) {
	accs, err := s.List()
	if err != nil {
		return Account{}, err
	}
	for _, a := range accs {
		if a.Slug == slug {
			return a, nil
		}
	}
	return Account{}, fmt.Errorf("no such account: %s", slug)
}

// Create records a new account and returns it, with a freshly generated keychain passphrase.
// The record is written BEFORE any login attempt: the passphrase has to be stable across the
// two calls a 2FA login takes, and a login that gets as far as "needs a code" must not lose it.
func (s *Accounts) Create(email string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	accs, err := s.loadLocked()
	if err != nil {
		return Account{}, err
	}
	slug := slugify(email)
	for _, a := range accs {
		if a.Slug == slug {
			// Re-adding an existing address reuses the record rather than making a
			// second one: the HOME directory, and therefore the session, is keyed by
			// slug, so a duplicate would be two names for one login.
			return a, nil
		}
	}

	pp, err := randomPassphrase()
	if err != nil {
		return Account{}, err
	}
	acc := Account{Slug: slug, Email: email, KeychainPP: pp, AddedAt: time.Now().UTC()}
	if err := os.MkdirAll(acc.Home(s.Root), 0o700); err != nil {
		return Account{}, err
	}
	if err := s.saveLocked(append(accs, acc)); err != nil {
		return Account{}, err
	}
	return acc, nil
}

// SetName records the display name ipatool reported after a successful login.
func (s *Accounts) SetName(slug, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accs, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range accs {
		if accs[i].Slug == slug {
			accs[i].Name = name
			return s.saveLocked(accs)
		}
	}
	return fmt.Errorf("no such account: %s", slug)
}

// Delete removes the record and the account's HOME, session cookies and all.
func (s *Accounts) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	accs, err := s.loadLocked()
	if err != nil {
		return err
	}
	kept := make([]Account, 0, len(accs))
	found := false
	for _, a := range accs {
		if a.Slug == slug {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("no such account: %s", slug)
	}
	if err := s.saveLocked(kept); err != nil {
		return err
	}
	// The session lives here. Removing the record without removing the directory would leave
	// a usable Apple ID session on disk under a name nothing references — SPEC §9's point
	// that a session cookie in /accounts/ is enough to download as that Apple ID.
	home := filepath.Join(s.Root, slug)
	if !isUnder(s.Root, home) {
		return fmt.Errorf("refusing to remove %s: outside the accounts root", home)
	}
	return os.RemoveAll(home)
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// slugify turns an email address into a directory name.
//
// The result is used as a PATH SEGMENT, so it is restricted to a known-safe alphabet rather
// than merely escaped: an address is user input, and `..` or a `/` in a directory name is how a
// tool ends up writing outside the root it thinks it owns.
func slugify(email string) string {
	s := strings.ToLower(strings.TrimSpace(email))
	s = strings.ReplaceAll(s, "@", "-at-")
	s = slugUnsafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" || s == "." || s == ".." {
		return "account"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// randomPassphrase generates the per-account keychain passphrase.
func randomPassphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeFileAtomic(dest string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dest)
}

// isUnder reports whether path is inside root. Used before any recursive delete.
func isUnder(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
