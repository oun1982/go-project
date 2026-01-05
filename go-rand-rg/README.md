# go-rand-rg

Small Go project. To upload to GitHub, create a repository and run:

```bash
git remote add origin <git@github.com:USER/go-rand-rg.git>
git branch -M main
git push -u origin main

```
# run commnad on linux for build to other linux 
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o random_rg_go -ldflags "-s -w" main.go
