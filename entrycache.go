package main

import (
	"database/sql"
	"sync"
	"time"
)

type EntryCache struct {
	sync.Mutex
	Recent []Entry
}

var entryCache EntryCache

func (cc *EntryCache) Init() {
	cc.Lock()
	defer cc.Unlock()

	rows, err := db.Query(`SELECT id, user_id, private, title, created_at FROM entries2 ORDER BY created_at DESC LIMIT 1000`)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	defer rows.Close()
	entries := make([]Entry, 0, 1000)
	for rows.Next() {
		var id, userID, private int
		var title string
		var createdAt time.Time
		checkErr(rows.Scan(&id, &userID, &private, &title, &createdAt))
		entries = append(entries, Entry{ID: id, UserID: userID, Private: private == 1, Title: title, CreatedAt: createdAt})
	}
	cc.Recent = entries

	for i := 0; i < len(cc.Recent)/2; i++ {
		cc.Recent[i], cc.Recent[len(cc.Recent)-1-i] = cc.Recent[len(cc.Recent)-1-i], cc.Recent[i]

	}
}

func (cc *EntryCache) Insert(e Entry) {
	cc.Lock()
	defer cc.Unlock()
	cc.Recent = append(cc.Recent, e)
	if len(cc.Recent) > 1000 {
		cc.Recent = cc.Recent[len(cc.Recent)-1000:]
	}
}

func (cc *EntryCache) Get() []Entry {
	cc.Lock()
	defer cc.Unlock()
	return cc.Recent
}
