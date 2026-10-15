# Platform-specific build variables and targets, included from the main
# Makefile so that file stays close to upstream.
.PHONY: check-ui-boundaries

# make's ** glob is not recursive, so ui/src/**/*.tsx missed deeper files and
# stale binaries were served as "up to date"; collect sources with find.
# .json/.css ride along: i18n-only or style-only edits must rebuild too.
UI_SOURCES := $(shell find $(UI_DIR)/src \( -name '*.tsx' -o -name '*.ts' -o -name '*.json' -o -name '*.css' \) 2>/dev/null) $(UI_DIR)/index.html $(UI_DIR)/portal.html

GO_SOURCES := $(shell find . -name '*.go' -not -path './ui/*' 2>/dev/null)

check-ui-boundaries: ## Check ui/src module boundary rules
	./scripts/check-ui-boundaries.sh
