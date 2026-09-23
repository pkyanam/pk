# Interval cover

`covercount` reads a JSON object from stdin and reports how many distinct
integer points are covered by the closed intervals in `ranges`.

Example:

```sh
printf '{"ranges":[{"start":1,"end":3},{"start":5,"end":6}]}\n' | go run ./cmd/covercount
```

Expected output: `{"covered":5}`.
