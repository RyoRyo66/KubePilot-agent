package platform

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const sessionLifetime = 12 * time.Hour
const passwordIterations = 600000

type passwordDigest struct {
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
}
type Account struct {
	ID        string         `json:"id"`
	Username  string         `json:"username"`
	Password  passwordDigest `json:"password"`
	CreatedAt time.Time      `json:"createdAt"`
}
type Workspace struct {
	ID                 string    `json:"id"`
	OwnerID            string    `json:"ownerId"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	CreatedAt          time.Time `json:"createdAt"`
	OnboardingComplete bool      `json:"onboardingComplete"`
	Legacy             bool      `json:"legacy,omitempty"`
}
type loginSession struct {
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}
type identityState struct {
	Schema     int                     `json:"schema"`
	Accounts   map[string]Account      `json:"accounts"`
	Workspaces map[string]Workspace    `json:"workspaces"`
	Sessions   map[string]loginSession `json:"sessions"`
}
type HubOptions struct {
	PublicOrigin       string
	RegistrationClosed bool
}
type rateBucket struct {
	Count int
	Until time.Time
}

// Hub owns identities and dispatches to a different Service for each workspace.
// The legacy root store is deliberately not opened until an administrator assigns it.
type Hub struct {
	mu            sync.Mutex
	dir           string
	store         *encryptedStore
	state         identityState
	services      map[string]*Service
	lock          *os.File
	options       HubOptions
	dummy         passwordDigest
	rates         map[string]rateBucket
	passwordSlots chan struct{}
	running       bool
}

func NewHub(dir string, options HubOptions) (_ *Hub, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := lockData(filepath.Join(dir, "accounts.lock"))
	if err != nil {
		return nil, fmt.Errorf("数据目录正在使用；维护账号前请先停止平台: %w", err)
	}
	h := &Hub{dir: dir, lock: lock, options: options, services: map[string]*Service{}, rates: map[string]rateBucket{}, passwordSlots: make(chan struct{}, 4)}
	defer func() {
		if err != nil {
			h.Close()
		}
	}()
	h.store, err = newStore(filepath.Join(dir, "identity"))
	if err != nil {
		return nil, err
	}
	err = h.store.loadInto(&h.state)
	if errors.Is(err, os.ErrNotExist) {
		h.state = identityState{Schema: 1, Accounts: map[string]Account{}, Workspaces: map[string]Workspace{}, Sessions: map[string]loginSession{}}
		err = h.store.save(h.state)
	}
	if err != nil {
		return nil, err
	}
	if h.state.Schema != 1 || h.state.Accounts == nil || h.state.Workspaces == nil || h.state.Sessions == nil {
		return nil, errors.New("账号数据版本或结构无效")
	}
	owners := map[string]bool{}
	for id, ws := range h.state.Workspaces {
		if id != ws.ID || !internalID.MatchString(id) || owners[ws.OwnerID] || h.state.Accounts[ws.OwnerID].ID == "" {
			return nil, errors.New("工作空间归属数据无效")
		}
		owners[ws.OwnerID] = true
		var svc *Service
		svc, err = New(h.workspaceDir(ws))
		if err != nil {
			return nil, err
		}
		h.services[id] = svc
	}
	h.dummy, err = hashPassword(randomID())
	if err != nil {
		return nil, err
	}
	return h, nil
}

var internalID = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)
var usernamePattern = regexp.MustCompile(`^[\p{L}\p{N}_-]{3,32}$`)

func normalizeUsername(s string) string {
	return strings.ToLower(norm.NFC.String(strings.TrimSpace(s)))
}
func validatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if !utf8.ValidString(p) || n < 12 || n > 128 || len(p) > 512 || strings.TrimSpace(p) == "" {
		return errors.New("密码需要 12–128 个字符，可以使用空格和符号")
	}
	return nil
}
func hashPassword(p string) (passwordDigest, error) {
	salt := []byte(randomID())
	b, err := pbkdf2.Key(sha256.New, p, salt, passwordIterations, 32)
	return passwordDigest{Algorithm: "pbkdf2-sha256", Iterations: passwordIterations, Salt: base64.RawStdEncoding.EncodeToString(salt), Hash: base64.RawStdEncoding.EncodeToString(b)}, err
}
func checkPassword(p string, d passwordDigest) bool {
	if len(p) > 512 || d.Algorithm != "pbkdf2-sha256" || d.Iterations != passwordIterations {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(d.Salt)
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(d.Hash)
	if err != nil || len(expected) != 32 {
		return false
	}
	b, err := pbkdf2.Key(sha256.New, p, salt, d.Iterations, 32)
	return err == nil && subtle.ConstantTimeCompare(b, expected) == 1
}
func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (h *Hub) workspaceDir(w Workspace) string {
	if w.Legacy {
		return h.dir
	}
	return filepath.Join(h.dir, "workspaces", w.ID)
}
func (h *Hub) saveLocked(next identityState) error {
	if err := h.store.save(next); err != nil {
		return err
	}
	h.state = next
	return nil
}
func (h *Hub) Run() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = true
	for _, s := range h.services {
		s.Run()
	}
}
func (h *Hub) Close() {
	for _, s := range h.services {
		s.Close()
	}
	if h.lock != nil {
		_ = h.lock.Close()
		h.lock = nil
	}
}
func (h *Hub) workspaceLocked(userID string) *Workspace {
	for _, ws := range h.state.Workspaces {
		if ws.OwnerID == userID {
			return &ws
		}
	}
	return nil
}
func (h *Hub) identity(token string) (Account, *Workspace, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.state.Sessions[sessionHash(token)]
	if !ok || token == "" || !s.ExpiresAt.After(time.Now()) {
		return Account{}, nil, false
	}
	a, ok := h.state.Accounts[s.UserID]
	return a, h.workspaceLocked(s.UserID), ok
}
func addSession(st *identityState, userID string) string {
	now := time.Now().UTC()
	var own []string
	for k, s := range st.Sessions {
		if !s.ExpiresAt.After(now) {
			delete(st.Sessions, k)
		} else if s.UserID == userID {
			own = append(own, k)
		}
	}
	for len(own) >= 5 {
		old := 0
		for i := range own {
			if st.Sessions[own[i]].CreatedAt.Before(st.Sessions[own[old]].CreatedAt) {
				old = i
			}
		}
		delete(st.Sessions, own[old])
		own = append(own[:old], own[old+1:]...)
	}
	token := randomID()
	st.Sessions[sessionHash(token)] = loginSession{UserID: userID, CreatedAt: now, ExpiresAt: now.Add(sessionLifetime)}
	return token
}
func (h *Hub) register(username, password, confirmation string) (string, error) {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return "", errors.New("用户名需要 3–32 个中英文字、数字、下划线或短横线")
	}
	if password != confirmation {
		return "", errors.New("两次输入的密码不一致")
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	d, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.state.Accounts {
		if a.Username == username {
			return "", errors.New("该用户名已被使用，请更换用户名或登录")
		}
	}
	next := clone(h.state)
	id := randomID()[:24]
	next.Accounts[id] = Account{ID: id, Username: username, Password: d, CreatedAt: time.Now().UTC()}
	token := addSession(&next, id)
	return token, h.saveLocked(next)
}
func (h *Hub) login(username, password string) (string, error) {
	h.mu.Lock()
	var a Account
	for _, candidate := range h.state.Accounts {
		if candidate.Username == normalizeUsername(username) {
			a = candidate
			break
		}
	}
	h.mu.Unlock()
	d := a.Password
	if a.ID == "" {
		d = h.dummy
	}
	valid := checkPassword(password, d)
	if !valid || a.ID == "" {
		return "", errors.New("用户名或密码错误")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// A concurrent reset must invalidate a login that verified the previous hash.
	if h.state.Accounts[a.ID].Password.Hash != a.Password.Hash {
		return "", errors.New("用户名或密码错误")
	}
	next := clone(h.state)
	token := addSession(&next, a.ID)
	return token, h.saveLocked(next)
}
func validateWorkspace(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 48 || utf8.RuneCountInString(description) > 200 || strings.ContainsAny(name, "\r\n\t") {
		return "", "", errors.New("工作空间名称为 1–48 个字符，描述最多 200 个字符")
	}
	return name, description, nil
}
func (h *Hub) createWorkspace(userID, name, description string) (*Workspace, error) {
	name, description, err := validateWorkspace(name, description)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.workspaceLocked(userID); old != nil {
		return old, nil
	} // Idempotent double-submit.
	ws := Workspace{ID: randomID()[:24], OwnerID: userID, Name: name, Description: description, CreatedAt: time.Now().UTC()}
	s, err := New(h.workspaceDir(ws))
	if err != nil {
		return nil, err
	}
	next := clone(h.state)
	next.Workspaces[ws.ID] = ws
	if err = h.saveLocked(next); err != nil {
		s.Close()
		return nil, err
	}
	h.services[ws.ID] = s
	if h.running {
		s.Run()
	}
	return &ws, nil
}
func (h *Hub) updateWorkspace(userID, name, description string) (*Workspace, error) {
	name, description, err := validateWorkspace(name, description)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ws := h.workspaceLocked(userID)
	if ws == nil {
		return nil, errors.New("请先创建工作空间")
	}
	ws.Name = name
	ws.Description = description
	next := clone(h.state)
	next.Workspaces[ws.ID] = *ws
	return ws, h.saveLocked(next)
}
func (h *Hub) changePassword(userID, current, password, confirmation string) error {
	if password != confirmation {
		return errors.New("两次输入的密码不一致")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	h.mu.Lock()
	a := h.state.Accounts[userID]
	h.mu.Unlock()
	if !checkPassword(current, a.Password) {
		return errors.New("当前密码错误")
	}
	d, err := hashPassword(password)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Accounts[userID].Password.Hash != a.Password.Hash {
		return errors.New("密码已发生变更，请重新登录")
	}
	next := clone(h.state)
	a.Password = d
	next.Accounts[userID] = a
	for k, s := range next.Sessions {
		if s.UserID == userID {
			delete(next.Sessions, k)
		}
	}
	return h.saveLocked(next)
}

// ResetPassword is a local, offline administration operation. NewHub holds an OS
// file lock, so maintenance cannot overwrite a running server's account state.
func (h *Hub) ResetPassword(username, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	d, err := hashPassword(password)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	next := clone(h.state)
	for id, a := range next.Accounts {
		if a.Username == normalizeUsername(username) {
			a.Password = d
			next.Accounts[id] = a
			for k, s := range next.Sessions {
				if s.UserID == id {
					delete(next.Sessions, k)
				}
			}
			return h.saveLocked(next)
		}
	}
	return errors.New("账号不存在")
}
func (h *Hub) ClaimLegacy(username string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ws := range h.state.Workspaces {
		if ws.Legacy {
			return errors.New("旧工作空间已分配，不能重复认领")
		}
	}
	var a Account
	for _, u := range h.state.Accounts {
		if u.Username == normalizeUsername(username) {
			a = u
			break
		}
	}
	if a.ID == "" {
		return errors.New("请先在网页注册指定账号")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "platform.enc")); err != nil {
		return errors.New("没有可认领的旧版本数据")
	}
	ws := h.workspaceLocked(a.ID)
	if ws != nil {
		st := h.services[ws.ID].current()
		if st.Config.PublicURL != "" || st.Config.APIKey != "" || st.Config.Generation > 0 || len(st.Alerts) > 0 || len(st.Cases) > 0 {
			return errors.New("目标工作空间已有接入配置或业务数据，拒绝覆盖")
		}
	} else {
		ws = &Workspace{ID: randomID()[:24], OwnerID: a.ID, Name: a.Username + "的工作空间", CreatedAt: time.Now().UTC()}
	}
	s, err := New(h.dir)
	if err != nil {
		return err
	}
	// Claiming transfers ownership, not permission to restart automatic writes.
	if err = s.change(func(st *DiskState) error {
		st.Config.Scope.Automatic = false
		event(st, "workspace.claimed", "旧工作空间已分配给指定账号，自动修复已暂停；请核对授权范围后重新开启")
		return nil
	}); err != nil {
		s.Close()
		return err
	}
	ws.Legacy = true
	next := clone(h.state)
	next.Workspaces[ws.ID] = *ws
	if err = h.saveLocked(next); err != nil {
		s.Close()
		return err
	}
	if old := h.services[ws.ID]; old != nil {
		old.Close()
	}
	h.services[ws.ID] = s
	return nil
}
