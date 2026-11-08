# herdsman up

Start a route: workspace, private inbox, both coordinators, the pair and the first brief.

```text
usage: herdsman up <route> -p <prompt> -a <command> -o <command> [-c <config>]
                   [--adjutant-kind <kind>] [--orchestrator-kind <kind>] [--no-adjutant]
                   [--pair auto|manual] [--web] [--no-attach] [--dry-run]
       herdsman up [<route>] --continue [--step <n>=done|rerun]...

-a and -o are the commands that launch each coordinator's harness, typed as you
would type them: "claude doctor && claude --model opus".
--dry-run prints the whole plan and starts nothing.
--web also starts the web app on the route.
--pair manual asks on the terminal before the pair is approved.
A second up on the same route picks up where the first stopped.
```

## Notes

- Run it inside a git repository.
- `--dry-run` needs the daemon already running. Without it the command exits 1 and names `herdsman daemon start`.
- A real start brings up the Herdr server and the daemon when they are down. A failed `herdsman doctor` check stops it.
- Bad usage, such as a missing `<route>`, `-p`, `-a` or `-o`, exits 2. `--no-adjutant` replaces `-a`.
