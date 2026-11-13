# make help: the target list from the Makefile's "##@ <section>" and
# "## <target> | <touches> | <dry run: plan, reads or -> | <help>[ | <variables>]" lines.
# A target with a dry run is marked with a dagger. Padding counts bytes, so the
# mark's width is added by hand rather than through printf.
function spaces(count, out) { out = ""; while (count-- > 0) out = out " "; return out }

/^##@ / { n++; section[n] = substr($0, 5); next }
/^## / {
	n++
	split(substr($0, 4), field, / [|] /)
	name[n] = field[1]; touches[n] = field[2]; dry[n] = field[3]; help[n] = field[4]; vars[n] = field[5]
	if (length(field[1]) > width) width = length(field[1])
}
END {
	print "Targets († takes DRY_RUN=true; variables in [] are optional; details in docs/commands.md):"
	for (i = 1; i <= n; i++) {
		if (i in section) { printf "\n%s:\n", section[i]; continue }
		mark = dry[i] == "-" ? "" : " †"
		pad = width + 2 - length(name[i]) - (mark == "" ? 0 : 2)
		printf "  %s%s%s  %s%s  %s\n", name[i], mark, spaces(pad), touches[i], spaces(19 - length(touches[i])), help[i]
		if (vars[i] != "") printf "  %s  %s  %s\n", spaces(width + 2), spaces(19), vars[i]
	}
}
