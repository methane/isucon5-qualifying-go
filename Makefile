app: app.go footprints.go
	GOOS=linux go build -o $@ $^
