.PHONY: check
check:
	python3 tools/check_docs.py
	@if [ -d tests ]; then python3 -m unittest discover -s tests; fi
