go run ${RACE:+-race} ./cmd/web -webport 11980 -data ./data-test-go-run-web -nntphostname "${NNTPHOST:?set NNTPHOST to a resolvable FQDN}" "$@"
