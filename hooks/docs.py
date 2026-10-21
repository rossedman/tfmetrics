"""MkDocs hooks that adapt generated and included Markdown to the site."""

import re

REPO = "https://github.com/rossedman/tfmetrics"

# Root-level files are included into docs pages with snippets. Their relative
# links point at repo files, so send them to GitHub.
ROOT_LINKS = re.compile(r"\]\((?!https?:|#|mailto:)(\.github/[^)]*|[A-Z_]+\.md)\)")


def on_page_markdown(markdown, page, **_):
    """Promote generated command headings and absolutize repo-file links."""
    src = page.file.src_uri
    if src.startswith("reference/commands/"):
        # cobra writes "## tfmetrics scan"; make it the page title.
        markdown = re.sub(r"\A## ", "# ", markdown, count=1)
        markdown = re.sub(r"^### ", "## ", markdown, flags=re.M)
    if src in ("contributing.md", "security.md"):
        markdown = ROOT_LINKS.sub(lambda m: f"]({REPO}/blob/main/{m.group(1)})", markdown)
    return markdown
