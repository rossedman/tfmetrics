"""MkDocs hooks that adapt generated and included Markdown to the site."""

import re
import tempfile
from pathlib import Path

REPO = "https://github.com/rossedman/tfmetrics"
INCLUDED = ("CONTRIBUTING.md", "SECURITY.md")

# Relative links in the root files point at repo files; send them to GitHub.
ROOT_LINKS = re.compile(r"\]\((?!https?:|#|mailto:)(\.github/[^)]*|[A-Za-z_]+\.md)\)")


def on_config(config):
    """Stage copies of the included root files with absolute repo links.

    The pages still include them with pymdownx.snippets; the staged directory
    is searched first, so the root files themselves are never changed.
    """
    stage = Path(tempfile.mkdtemp(prefix="tfmetrics-docs-"))
    for name in INCLUDED:
        src = Path(config.config_file_path).parent / name
        if not src.exists():
            continue
        text = ROOT_LINKS.sub(lambda m: f"]({REPO}/blob/main/{m.group(1)})", src.read_text())
        (stage / name).write_text(text)
    snippets = config["mdx_configs"].setdefault("pymdownx.snippets", {})
    base = snippets.get("base_path", ["."])
    snippets["base_path"] = [str(stage), *([base] if isinstance(base, str) else base)]
    return config


def on_page_markdown(markdown, page, **_):
    """Promote the headings of generated command pages."""
    if page.file.src_uri.startswith("reference/commands/"):
        # cobra writes "## tfmetrics scan"; make it the page title.
        markdown = re.sub(r"\A## ", "# ", markdown, count=1)
        markdown = re.sub(r"^### ", "## ", markdown, flags=re.M)
    return markdown
