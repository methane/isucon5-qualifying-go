package main

import ()

func markFootprint(visitor, id int) {
	if visitor != id {
		_, err := db.Exec(`INSERT INTO footprints (user_id,owner_id) VALUES (?,?)`, id, visitor)
		if err != nil {
			panic(err)
		}
	}
}
