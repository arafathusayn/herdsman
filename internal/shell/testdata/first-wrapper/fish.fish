function herdsman
    set -l directive (mktemp)
    or return 1
    command env HERDSMAN_DIRECTIVE_FILE=$directive herdsman $argv
    set -l code $status
    set -l target
    for line in (cat $directive)
        if string match -q '/*' -- $line
            set target $line
        end
    end
    rm -f $directive
    if test -n "$target"; and test -d "$target"
        cd $target
    end
    return $code
end
