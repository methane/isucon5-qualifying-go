package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/context"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
)

const UnixPath = "/tmp/isuxi-app.sock"

var (
	db    *sql.DB
	store *sessions.CookieStore
)

type User struct {
	ID          int
	AccountName string
	NickName    string
	Email       string
}

type UserRepo struct {
	sync.RWMutex
	users     map[int]*User
	byMail    map[string]int
	byAccount map[string]int
}

func (r *UserRepo) Init() {
	r.Lock()
	defer r.Unlock()

	r.users = make(map[int]*User, 1024)
	r.byMail = make(map[string]int, 1024)
	r.byAccount = make(map[string]int, 1024)
	rows, err := db.Query(`SELECT id, account_name, nick_name, email FROM users`)
	if err != nil && err != sql.ErrNoRows {
		log.Fatal(err)
	}
	for rows.Next() {
		var u User
		err = rows.Scan(&u.ID, &u.AccountName, &u.NickName, &u.Email)
		if err != nil {
			log.Fatal(err)
		}
		r.users[u.ID] = &u
		r.byMail[u.Email] = u.ID
		r.byAccount[u.AccountName] = u.ID
	}
	rows.Close()
}

func (r *UserRepo) Get(id int) *User {
	r.RLock()
	u := r.users[id]
	r.RUnlock()
	return u
}

func (r *UserRepo) GetByMail(email string) *User {
	var u *User
	r.RLock()
	uid := r.byMail[email]
	if uid != 0 {
		u = r.users[uid]
	}
	r.RUnlock()
	return u
}

func (r *UserRepo) GetByAccount(account string) *User {
	var u *User
	r.RLock()
	uid := r.byAccount[account]
	if uid != 0 {
		u = r.users[uid]
	}
	r.RUnlock()
	return u
}

var userRepo = UserRepo{}

type Profile struct {
	UserID    int
	FirstName string
	LastName  string
	Sex       string
	Birthday  mysql.NullTime
	Pref      string
	UpdatedAt time.Time
}

type ProfileRepo struct {
	sync.Mutex
	profiles map[int]*Profile
}

func (r *ProfileRepo) Get(id int) *Profile {
	r.Lock()
	defer r.Unlock()
	return r.profiles[id]
}

func (r *ProfileRepo) Update(id int, prof *Profile) {
	r.Lock()
	defer r.Unlock()
	r.profiles[id] = prof
}

func (r *ProfileRepo) Init() {
	r.Lock()
	defer r.Unlock()
	r.profiles = make(map[int]*Profile, 1000)

	rows, err := db.Query(`SELECT * FROM profiles`)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		prof := Profile{}
		err := rows.Scan(&prof.UserID, &prof.FirstName, &prof.LastName, &prof.Sex, &prof.Birthday, &prof.Pref, &prof.UpdatedAt)
		if err != nil {
			panic(err)
		}
		r.profiles[prof.UserID] = &prof
	}
	rows.Close()
}

var profileRepo = &ProfileRepo{}

type Entry struct {
	ID          int
	UserID      int
	Private     bool
	Title       string
	Content     string
	CreatedAt   time.Time
	NumComments int
}

type Friend struct {
	ID        int
	CreatedAt time.Time
}

type FriendRepo struct {
	sync.Mutex
	friend map[int]map[int]bool
}

var friendRepo = FriendRepo{friend: make(map[int]map[int]bool, 1024)}

func (fr *FriendRepo) Reset() {
	fr.Lock()
	fr.friend = make(map[int]map[int]bool, 1024)
	fr.Unlock()
}

func (fr *FriendRepo) Insert(a, b int) {
	if a == b {
		return
	}
	fr.Lock()
	aa := fr.friend[a]
	if aa == nil {
		aa = make(map[int]bool)
		fr.friend[a] = aa
	}
	aa[b] = true
	bb := fr.friend[b]
	if bb == nil {
		bb = make(map[int]bool)
		fr.friend[b] = bb
	}
	bb[a] = true
	fr.Unlock()
}

func (fr *FriendRepo) IsFriend(a, b int) bool {
	if a == b {
		return true
	}
	fr.Lock()
	defer fr.Unlock()
	aa := fr.friend[a]
	if aa == nil {
		return false
	}
	return aa[b]
}

func (fr *FriendRepo) Count(userID int) int {
	fr.Lock()
	c := 0
	m := fr.friend[userID]
	if m != nil {
		c = len(m)
	}
	fr.Unlock()
	return c
}

func (fr *FriendRepo) Init() {
	fr.Reset()
	rows, err := db.Query(`SELECT one, another FROM relations`)
	if err != sql.ErrNoRows && err != nil {
		panic(err)
	}
	for rows.Next() {
		var a, b int
		rows.Scan(&a, &b)
		fr.Insert(a, b)
	}
	rows.Close()
}

type Comment struct {
	ID           int
	EntryID      int
	UserID       int
	Comment      string
	CreatedAt    time.Time
	EntryOwnerID int
	private      bool
}

type CommentCache struct {
	sync.Mutex
	Recent []Comment
}

var commentCache CommentCache

func (cc *CommentCache) Init() {
	cc.Lock()
	defer cc.Unlock()
	rows, err := db.Query(`SELECT c.id, entry_id, c.user_id, comment, c.created_at, e.user_id, e.private
FROM comments as c LEFT JOIN entries2 as e ON (entry_id=e.id) ORDER BY c.created_at DESC LIMIT 1000`)
	if err != nil {
		panic(err)
	}

	cc.Recent = make([]Comment, 0, 1000)
	for rows.Next() {
		c := Comment{}
		checkErr(rows.Scan(&c.ID, &c.EntryID, &c.UserID, &c.Comment, &c.CreatedAt, &c.EntryOwnerID, &c.private))
		cc.Recent = append(cc.Recent, c)
	}
	for i := 0; i < len(cc.Recent)/2; i++ {
		cc.Recent[i], cc.Recent[len(cc.Recent)-1-i] = cc.Recent[len(cc.Recent)-1-i], cc.Recent[i]
	}
	rows.Close()
}

func (cc *CommentCache) Insert(c Comment) {
	cc.Lock()
	defer cc.Unlock()
	cc.Recent = append(cc.Recent, c)
	if len(cc.Recent) > 1000 {
		cc.Recent = cc.Recent[len(cc.Recent)-1000:]
	}
}

func (cc *CommentCache) Get() []Comment {
	cc.Lock()
	defer cc.Unlock()
	return cc.Recent
}

var prefs = []string{"未入力",
	"北海道", "青森県", "岩手県", "宮城県", "秋田県", "山形県", "福島県", "茨城県", "栃木県", "群馬県", "埼玉県", "千葉県", "東京都", "神奈川県", "新潟県", "富山県",
	"石川県", "福井県", "山梨県", "長野県", "岐阜県", "静岡県", "愛知県", "三重県", "滋賀県", "京都府", "大阪府", "兵庫県", "奈良県", "和歌山県", "鳥取県", "島根県",
	"岡山県", "広島県", "山口県", "徳島県", "香川県", "愛媛県", "高知県", "福岡県", "佐賀県", "長崎県", "熊本県", "大分県", "宮崎県", "鹿児島県", "沖縄県"}

var (
	ErrContentNotFound = errors.New("Content not found.")
)

func authenticationFailed(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	delete(session.Values, "user_id")
	session.Save(r, w)
	render(w, r, http.StatusUnauthorized, "login.html", struct{ Message string }{"ログインに失敗しました"})
}

func getProfile(id int) *Profile {
	prof := Profile{}
	row := db.QueryRow(`SELECT * FROM profiles WHERE user_id = ?`, id)
	err := row.Scan(&prof.UserID, &prof.FirstName, &prof.LastName, &prof.Sex, &prof.Birthday, &prof.Pref, &prof.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return &prof
}

func authenticate(w http.ResponseWriter, r *http.Request, email, passwd string) {
	u := userRepo.GetByMail(email)
	if u == nil {
		authenticationFailed(w, r)
		return
	}
	session := getSession(w, r)
	session.Values["user_id"] = u.ID
	session.Save(r, w)
}

func permissionDenied(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusForbidden, "error.html", struct{ Message string }{"友人のみしかアクセスできません"})
}

func getCurrentUser(w http.ResponseWriter, r *http.Request) *User {
	u := context.Get(r, "user")
	if u != nil {
		user := u.(*User)
		return user
	}
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok || userID == nil {
		return nil
	}
	user := userRepo.Get(userID.(int))
	context.Set(r, "user", user)
	return user
}

func authenticated(w http.ResponseWriter, r *http.Request) bool {
	user := getCurrentUser(w, r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return false
	}
	return true
}

func getUser(userID int) *User {
	return userRepo.Get(userID)
}

func getUserFromAccount(w http.ResponseWriter, name string) *User {
	return userRepo.GetByAccount(name)
}

func isFriend(w http.ResponseWriter, r *http.Request, anotherID int) bool {
	session := getSession(w, r)
	id := session.Values["user_id"].(int)
	return friendRepo.IsFriend(id, anotherID)
}

func isFriendAccount(w http.ResponseWriter, r *http.Request, name string) bool {
	user := userRepo.GetByAccount(name)
	if user == nil {
		return false
	}
	return isFriend(w, r, user.ID)
}

func permitted(w http.ResponseWriter, r *http.Request, anotherID int) bool {
	user := getCurrentUser(w, r)
	if anotherID == user.ID {
		return true
	}
	return isFriend(w, r, anotherID)
}
func permitted2(myID, anotherID int) bool {
	if myID == anotherID {
		return true
	}
	return friendRepo.IsFriend(myID, anotherID)
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, "isucon5q-go.session")
	return session
}

func getTemplatePath(file string) string {
	return path.Join("templates", file)
}

var templates map[string]*template.Template

func initTemplate(t string, fm template.FuncMap) {
	tpl := template.Must(template.New(t).Funcs(fm).ParseFiles(getTemplatePath(t), getTemplatePath("header.html")))
	templates[t] = tpl
}

func init() {
	templates = make(map[string]*template.Template)
	fmap := template.FuncMap{
		"getUser": getUser,
		"prefectures": func() []string {
			return prefs
		},
		"substring": func(s string, l int) string {
			if len(s) > l {
				return s[:l]
			}
			return s
		},
		"split": strings.Split,
	}

	templates_str := "entries.html entry.html error.html footprints.html friends.html index.html login.html profile.html"
	templates := strings.Split(templates_str, " ")
	for _, t := range templates {
		initTemplate(t, fmap)
	}
}

func render(w http.ResponseWriter, r *http.Request, status int, file string, data interface{}) {
	tpl := templates[file]
	w.WriteHeader(status)
	checkErr(tpl.Execute(w, data))
}

func GetLogin(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, "login.html", struct{ Message string }{"高負荷に耐えられるSNSコミュニティサイトへようこそ!"})
}

func PostLogin(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	passwd := r.FormValue("password")
	authenticate(w, r, email, passwd)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func GetLogout(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	delete(session.Values, "user_id")
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func GetIndex(w http.ResponseWriter, r *http.Request) {
	user := getCurrentUser(w, r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	prof := profileRepo.Get(user.ID)

	rows, err := db.Query(`SELECT id, title FROM entries2 WHERE user_id = ? ORDER BY created_at LIMIT 5`, user.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	entries := make([]Entry, 0, 5)
	for rows.Next() {
		var id int
		var title string
		checkErr(rows.Scan(&id, &title))
		entries = append(entries, Entry{ID: id, Title: title})
	}
	rows.Close()

	rows, err = db.Query(`SELECT c.id AS id, c.entry_id AS entry_id, c.user_id AS user_id, c.comment AS comment, c.created_at AS created_at
FROM comments c
WHERE c.entry_user_id = ?
ORDER BY c.created_at DESC
LIMIT 10`, user.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	commentsForMe := make([]Comment, 0, 10)
	for rows.Next() {
		c := Comment{}
		checkErr(rows.Scan(&c.ID, &c.EntryID, &c.UserID, &c.Comment, &c.CreatedAt))
		commentsForMe = append(commentsForMe, c)
	}
	rows.Close()

	recentEntries := entryCache.Get()
	entriesOfFriends := make([]Entry, 0, 10)
	for i := len(recentEntries) - 1; i >= 0; i-- {
		e := recentEntries[i]
		if !friendRepo.IsFriend(user.ID, e.UserID) {
			continue
		}
		entriesOfFriends = append(entriesOfFriends, e)
		if len(entriesOfFriends) >= 10 {
			break
		}
	}

	commentsOfFriends := make([]Comment, 0, 10)
	cc := commentCache.Get()
	for i := len(cc) - 1; i >= 0; i-- {
		c := cc[i]
		if !friendRepo.IsFriend(user.ID, c.UserID) {
			continue
		}
		if c.private {
			if !permitted2(user.ID, c.EntryOwnerID) {
				continue
			}
		}
		commentsOfFriends = append(commentsOfFriends, c)
		if len(commentsOfFriends) >= 10 {
			break
		}
	}

	footprints := footPrintCache.Get(user.ID)[:10]

	render(w, r, http.StatusOK, "index.html", struct {
		User              User
		Profile           *Profile
		Entries           []Entry
		CommentsForMe     []Comment
		FriendEntries     template.HTML
		CommentsOfFriends template.HTML
		NumFriends        int
		Footprints        []Footprint
	}{
		*user, prof, entries, commentsForMe, renderFriendEntries(entriesOfFriends),
		renderCommentsOfFriends(commentsOfFriends), friendRepo.Count(user.ID), footprints,
	})
}

func GetProfile(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	currentUser := getCurrentUser(w, r)

	account := mux.Vars(r)["account_name"]
	owner := getUserFromAccount(w, account)
	prof := profileRepo.Get(owner.ID)

	var query string
	if permitted2(currentUser.ID, owner.ID) {
		query = `SELECT * FROM entries2 WHERE user_id = ? ORDER BY created_at LIMIT 5`
	} else {
		query = `SELECT * FROM entries2 WHERE user_id = ? AND private=0 ORDER BY created_at LIMIT 5`
	}
	rows, err := db.Query(query, owner.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	entries := make([]Entry, 0, 5)
	for rows.Next() {
		var id, userID, private int
		var title, body string
		var createdAt time.Time
		checkErr(rows.Scan(&id, &userID, &private, &title, &body, &createdAt))
		entry := Entry{id, userID, private == 1, title, body, createdAt, 0}
		entries = append(entries, entry)
	}
	rows.Close()

	markFootprint(currentUser.ID, owner.ID)

	render(w, r, http.StatusOK, "profile.html", struct {
		Owner       *User
		Profile     *Profile
		Entries     []Entry
		Private     bool
		CurrentUser *User
		IsFriend    bool
	}{
		owner, prof, entries, permitted2(currentUser.ID, owner.ID), currentUser, isFriend(w, r, owner.ID),
	})
}

func PostProfile(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	user := getCurrentUser(w, r)
	account := mux.Vars(r)["account_name"]
	if account != user.AccountName {
		permissionDenied(w, r)
		return
	}
	query := `UPDATE profiles
SET first_name=?, last_name=?, sex=?, birthday=?, pref=?, updated_at=CURRENT_TIMESTAMP()
WHERE user_id = ?`
	birth := r.FormValue("birthday")
	firstName := r.FormValue("first_name")
	lastName := r.FormValue("last_name")
	sex := r.FormValue("sex")
	pref := r.FormValue("pref")
	_, err := db.Exec(query, firstName, lastName, sex, birth, pref, user.ID)
	checkErr(err)

	prof := Profile{}
	row := db.QueryRow("SELECT * FROM profiles WHERE user_id=?", user.ID)
	err = row.Scan(&prof.UserID, &prof.FirstName, &prof.LastName, &prof.Sex, &prof.Birthday, &prof.Pref, &prof.UpdatedAt)
	if err != nil {
		panic(err)
	}

	// TODO should escape the account name?
	profileRepo.Update(user.ID, &prof)
	http.Redirect(w, r, "/profile/"+account, http.StatusSeeOther)
}

func ListEntries(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	myID := getCurrentUser(w, r).ID

	account := mux.Vars(r)["account_name"]
	owner := getUserFromAccount(w, account)
	var query string
	const select_expr = `SELECT id, user_id, private, title, body, created_at, (select count(*) FROM comments WHERE entry_id=entries2.id) FROM entries2 `
	if permitted2(myID, owner.ID) {
		query = select_expr + `WHERE user_id = ? ORDER BY created_at DESC LIMIT 20`
	} else {
		query = select_expr + `WHERE user_id = ? AND private=0 ORDER BY created_at DESC LIMIT 20`
	}
	rows, err := db.Query(query, owner.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	entries := make([]Entry, 0, 20)
	for rows.Next() {
		var id, userID, private int
		var title, body string
		var createdAt time.Time
		var nc int
		checkErr(rows.Scan(&id, &userID, &private, &title, &body, &createdAt, &nc))
		entry := Entry{id, userID, private == 1, title, body, createdAt, nc}
		entries = append(entries, entry)
	}
	rows.Close()

	currentUser := getCurrentUser(w, r)
	markFootprint(currentUser.ID, owner.ID)

	render(w, r, http.StatusOK, "entries.html", struct {
		Owner   *User
		Myself  bool
		Entries template.HTML
	}{owner, currentUser.ID == owner.ID, renderEntriesList(entries)})
}

func GetEntry(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	entryID := mux.Vars(r)["entry_id"]
	row := db.QueryRow(`SELECT * FROM entries2 WHERE id = ?`, entryID)
	var id, userID, private int
	var title, body string
	var createdAt time.Time
	err := row.Scan(&id, &userID, &private, &title, &body, &createdAt)
	if err == sql.ErrNoRows {
		render(w, r, http.StatusNotFound, "error.html", struct{ Message string }{"要求されたコンテンツは存在しません"})
		return
	}
	checkErr(err)
	entry := Entry{id, userID, private == 1, title, body, createdAt, 0}
	owner := getUser(entry.UserID)
	if entry.Private {
		if !permitted(w, r, owner.ID) {
			permissionDenied(w, r)
			return
		}
	}
	rows, err := db.Query(`SELECT id, entry_id, user_id, comment, created_at FROM comments WHERE entry_id = ?`, entry.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	comments := make([]Comment, 0, 10)
	for rows.Next() {
		c := Comment{}
		checkErr(rows.Scan(&c.ID, &c.EntryID, &c.UserID, &c.Comment, &c.CreatedAt))
		comments = append(comments, c)
	}
	rows.Close()

	currentUser := getCurrentUser(w, r)
	markFootprint(currentUser.ID, owner.ID)

	render(w, r, http.StatusOK, "entry.html", struct {
		Owner    *User
		Entry    Entry
		Comments []Comment
	}{owner, entry, comments})
}

func PostEntry(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}

	user := getCurrentUser(w, r)
	title := r.FormValue("title")
	if title == "" {
		title = "タイトルなし"
	}
	content := r.FormValue("content")
	var private int
	if r.FormValue("private") == "" {
		private = 0
	} else {
		private = 1
	}
	_, err := db.Exec(`INSERT INTO entries2 (user_id, private, title, body) VALUES (?,?,?,?)`, user.ID, private, title, content)
	checkErr(err)
	http.Redirect(w, r, "/diary/entries/"+user.AccountName, http.StatusSeeOther)
}

func PostComment(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}

	entryID := mux.Vars(r)["entry_id"]
	row := db.QueryRow(`SELECT id, user_id, private FROM entries2 WHERE id = ?`, entryID)
	var id, userID, private int
	err := row.Scan(&id, &userID, &private)
	if err == sql.ErrNoRows {
		render(w, r, http.StatusNotFound, "error.html", struct{ Message string }{"要求されたコンテンツは存在しません"})
		return
	}
	checkErr(err)

	entry := Entry{ID: id, UserID: userID, Private: private == 1}
	owner := getUser(entry.UserID)
	if entry.Private {
		if !permitted(w, r, owner.ID) {
			permissionDenied(w, r)
		}
	}
	user := getCurrentUser(w, r)

	result, err := db.Exec(`INSERT INTO comments (entry_id, user_id, comment, entry_user_id) VALUES (?,?,?,?)`, entry.ID, user.ID, r.FormValue("comment"), entry.UserID)
	checkErr(err)
	lastId, _ := result.LastInsertId()
	c := Comment{ID: int(lastId), EntryID: entry.ID, UserID: user.ID, Comment: r.FormValue("comment"), CreatedAt: time.Now(), EntryOwnerID: entry.UserID, private: entry.Private}
	commentCache.Insert(c)
	http.Redirect(w, r, "/diary/entry/"+strconv.Itoa(entry.ID), http.StatusSeeOther)
}

func GetFootprints(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	user := getCurrentUser(w, r)
	footprints := footPrintCache.Get(user.ID)
	render(w, r, http.StatusOK, "footprints.html",
		struct{ Footprints []Footprint }{footprints[:50]})
}

func GetFriends(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}
	user := getCurrentUser(w, r)
	rows, err := db.Query(`SELECT * FROM relations WHERE one = ? ORDER BY created_at DESC`, user.ID)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	friendsMap := make(map[int]time.Time)
	for rows.Next() {
		var id, one, another int
		var createdAt time.Time
		checkErr(rows.Scan(&id, &one, &another, &createdAt))
		var friendID int
		if one == user.ID {
			friendID = another
		} else {
			friendID = one
		}
		if _, ok := friendsMap[friendID]; !ok {
			friendsMap[friendID] = createdAt
		}
	}
	rows.Close()
	friends := make([]Friend, 0, len(friendsMap))
	for key, val := range friendsMap {
		friends = append(friends, Friend{key, val})
	}
	render(w, r, http.StatusOK, "friends.html", struct{ Friends []Friend }{friends})
}

func PostFriends(w http.ResponseWriter, r *http.Request) {
	if !authenticated(w, r) {
		return
	}

	user := getCurrentUser(w, r)
	anotherAccount := mux.Vars(r)["account_name"]
	if !isFriendAccount(w, r, anotherAccount) {
		another := getUserFromAccount(w, anotherAccount)
		_, err := db.Exec(`INSERT INTO relations (one, another) VALUES (?,?), (?,?)`, user.ID, another.ID, another.ID, user.ID)
		checkErr(err)
		friendRepo.Insert(user.ID, another.ID)
		http.Redirect(w, r, "/friends", http.StatusSeeOther)
	}
}

func GetInitialize(w http.ResponseWriter, r *http.Request) {
	db.Exec("DELETE FROM relations WHERE id > 500000")
	db.Exec("DELETE FROM footprints WHERE id > 500000")
	db.Exec("DELETE FROM entries2 WHERE id > 500000")
	db.Exec("DELETE FROM comments WHERE id > 1500000")
	friendRepo.Init()
	commentCache.Init()
	userRepo.Init()
	footPrintCache.Reset()
	entryCache.Init()
	profileRepo.Init()
	//db.Exec("SELECT title FROM entries2 ORDER BY id desc LIMIT 10000")
}

func main() {
	var err error
	for {
		db, err = sql.Open("mysql", "root@unix(/var/run/mysqld/mysqld.sock)/isucon5q?loc=Local&parseTime=true&interpolateParams=true")
		//db, err = sql.Open("mysql", "root@tcp(127.0.0.1:3306)/isucon5q?loc=Local&parseTime=true&interpolateParams=true")
		if err != nil {
			log.Println("Failed to open DB: %s.", err.Error())
			time.Sleep(time.Second)
			continue
		}
		err = db.Ping()
		if err != nil {
			log.Println("Failed to connect to DB: %s.", err)
			db.Close()
			time.Sleep(time.Second)
			continue
		}
		break
	}
	db.SetMaxIdleConns(50)
	defer db.Close()

	ssecret := os.Getenv("ISUCON5_SESSION_SECRET")
	if ssecret == "" {
		ssecret = "beermoris"
	}
	store = sessions.NewCookieStore([]byte(ssecret))

	r := mux.NewRouter()

	l := r.Path("/login").Subrouter()
	l.Methods("GET").HandlerFunc(http.HandlerFunc(GetLogin))
	l.Methods("POST").HandlerFunc(http.HandlerFunc(PostLogin))
	r.Path("/logout").Methods("GET").HandlerFunc(http.HandlerFunc(GetLogout))

	p := r.Path("/profile/{account_name}").Subrouter()
	p.Methods("GET").HandlerFunc(http.HandlerFunc(GetProfile))
	p.Methods("POST").HandlerFunc(http.HandlerFunc(PostProfile))

	d := r.PathPrefix("/diary").Subrouter()
	d.HandleFunc("/entries/{account_name}", http.HandlerFunc(ListEntries)).Methods("GET")
	d.HandleFunc("/entry", http.HandlerFunc(PostEntry)).Methods("POST")
	d.HandleFunc("/entry/{entry_id}", http.HandlerFunc(GetEntry)).Methods("GET")

	d.HandleFunc("/comment/{entry_id}", http.HandlerFunc(PostComment)).Methods("POST")

	r.HandleFunc("/footprints", http.HandlerFunc(GetFootprints)).Methods("GET")

	r.HandleFunc("/friends", http.HandlerFunc(GetFriends)).Methods("GET")
	r.HandleFunc("/friends/{account_name}", http.HandlerFunc(PostFriends)).Methods("POST")

	r.HandleFunc("/initialize", http.HandlerFunc(GetInitialize))
	r.HandleFunc("/", http.HandlerFunc(GetIndex))
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("../static")))
	friendRepo.Init()
	commentCache.Init()
	userRepo.Init()
	entryCache.Init()
	profileRepo.Init()
	go http.ListenAndServe(":3000", nil)
	go http.ListenAndServe(":8080", r)
	os.Remove(UnixPath)
	ul, err := net.Listen("unix", UnixPath)
	if err != nil {
		panic(err)
	}
	os.Chmod(UnixPath, 0777)
	defer ul.Close()
	log.Fatal(http.Serve(ul, r))
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func renderFriendEntries(es []Entry) template.HTML {
	const t1 = `
  <div class="col-md-4">
    <div>あなたの友だちの日記エントリ</div>
    <div id="friend-entries">`
	const tx = `</div></div>`

	buff := &bytes.Buffer{}
	buff.WriteString(t1)
	for _, e := range es {
		//{{ range .EntriesOfFriends }}
		const t2 = `<div class="friend-entry">
<ul class="list-group">
`
		buff.WriteString(t2)
		//    {{ $entryOwner := getUser .UserID }}
		owner := getUser(e.UserID)
		fmt.Fprintf(buff, `
    <li class="list-group-item entry-owner"><a href="/diary/entries/%s">%sさん</a>:</li>
    <li class="list-group-item entry-title"><a href="/diary/entry/%v">%s</a></li>
    <li class="list-group-item entry-created-at">投稿時刻:%s</li>
		  </ul>
		</div>
`, owner.AccountName, owner.NickName,
			e.ID, template.HTMLEscapeString(e.Title),
			e.CreatedAt.Format("2006-01-02 15:04:05"))
		//{{ end }}
	}
	buff.WriteString(tx)
	return template.HTML(buff.String())
}

func renderCommentsOfFriends(comments []Comment) template.HTML {
	buf := &bytes.Buffer{}
	buf.WriteString(`
  <div class="col-md-4">
    <div>あなたの友だちのコメント</div>
    <div id="friend-comments">`)

	for _, c := range comments {
		cowner := getUser(c.UserID)
		eowner := getUser(c.EntryOwnerID)
		comment := c.Comment
		if len(comment) > 30 {
			comment = comment[:27] + "..."
		}
		fmt.Fprintf(buf, `
      <div class="friend-comment">
        <ul class="list-group">
          <li class="list-group-item comment-from-to"><a href="/profile/%s">%sさん</a>から<a href="/profile/%s">%sさん</a>へのコメント:</li>
          <li class="list-group-item comment-comment">%s</li>
          <li class="list-group-item comment-created-at">投稿時刻:%s</li>
        </ul>
      </div>`, cowner.AccountName, template.HTMLEscapeString(cowner.NickName), eowner.AccountName, template.HTMLEscapeString(eowner.NickName),
			template.HTMLEscapeString(comment), c.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	buf.WriteString(`</div></div>`)
	return template.HTML(buf.String())
}

func renderEntriesList(entries []Entry) template.HTML {
	buf := &bytes.Buffer{}
	buf.WriteString(`
<div class="row" id="entries">`)
	for _, e := range entries {
		title := template.HTMLEscapeString(e.Title)
		content := template.HTMLEscapeString(e.Content)
		content = strings.Replace(content, "\n", "<br />\n", 0)
		fmt.Fprintf(buf, `
    <div class="panel panel-primary entry">
        <div class="entry-title">タイトル: <a href="/diary/entry/%d">%s</a></div>
        <div class="entry-content">
%s
        </div>
	`, e.ID, title, content)
		if e.Private {
			buf.WriteString(`<div class="text-danger entry-private">範囲: 友だち限定公開</div>`)
		}
		fmt.Fprintf(buf, `
        <div class="entry-created-at">更新日時: %s</div>
        <div class="entry-comments">コメント: %d件</div>
    </div>`,
			e.CreatedAt.Format("2006-01-02 15:04:05"), e.NumComments)
	}
	buf.WriteString(`</div>`)
	return template.HTML(buf.String())
}
