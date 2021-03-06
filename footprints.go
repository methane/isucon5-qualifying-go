package main

import (
	"database/sql"
	"sync"
	"time"
)

type Footprint struct {
	UserID    int       // 踏まれた人
	OwnerID   int       // 踏んだ人
	CreatedAt time.Time // date
	UpdatedAt time.Time // time
}

type FoopprintCache struct {
	sync.Mutex
	cache map[int][]Footprint
}

var footPrintCache = FoopprintCache{cache: make(map[int][]Footprint, 1024)}

func (c *FoopprintCache) Reset() {
	c.Lock()
	c.cache = make(map[int][]Footprint, 1024)
	c.Unlock()
}

func (c *FoopprintCache) Get(userID int) []Footprint {
	c.Lock()
	defer c.Unlock()
	if fps, ok := c.cache[userID]; ok {
		return fps
	}

	fps := fetchFootprint(userID, 50)
	c.cache[userID] = fps
	return fps
}

func (c *FoopprintCache) Invalidate(userID int) {
	c.Lock()
	delete(c.cache, userID)
	c.Unlock()
}

func markFootprint(visitor, id int) {
	if visitor != id {
		now := time.Now()
		_, err := db.Exec(`replace INTO footprints (user_id,owner_id,date,created_at) VALUES (?,?,?,?)`, id, visitor, now, now)
		if err != nil {
			panic(err)
		}
		footPrintCache.Invalidate(id)
	}
}

func fetchFootprint(userID, limit int) []Footprint {
	rows, err := db.Query(`SELECT user_id, owner_id, date, created_at
FROM footprints
WHERE user_id = ?
ORDER BY created_at DESC
LIMIT ?`, userID, limit)
	if err != sql.ErrNoRows {
		checkErr(err)
	}
	defer rows.Close()
	footprints := make([]Footprint, 0, 10)
	for rows.Next() {
		fp := Footprint{}
		checkErr(rows.Scan(&fp.UserID, &fp.OwnerID, &fp.CreatedAt, &fp.UpdatedAt))
		footprints = append(footprints, fp)
	}
	return footprints
}
