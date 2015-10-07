app: app.go footprints.go entrycache.go
	GOOS=linux go build -o $@ $^

send:
	scp -C app isucon:webapp/go/app2
	rsync -avK templates/ isucon:webapp/go/templates/
