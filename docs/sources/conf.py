# Configuration file for the Sphinx documentation builder.

# -- Project information -----------------------------------------------------

project = "cloud-vinyl"
copyright = "2026, BlueDynamics Alliance"  # noqa: A001
author = "BlueDynamics Alliance"
release = "0.1.0"

# -- General configuration ---------------------------------------------------

extensions = [
    "myst_parser",
    "sphinxcontrib.mermaid",
    "sphinx_design",
    "sphinx_copybutton",
]

myst_enable_extensions = [
    "deflist",
    "colon_fence",
    "fieldlist",
]

myst_fence_as_directive = ["mermaid"]

templates_path = ["_templates"]
exclude_patterns = []

# mermaid options
mermaid_output_format = "raw"

# -- Options for HTML output -------------------------------------------------

html_theme = "shibuya"

html_theme_options = {
    "logo_target": "/cloud-vinyl/",
    "accent_color": "teal",
    "color_mode": "dark",
    "dark_code": True,
    "nav_links": [
        {
            "title": "GitHub",
            "url": "https://github.com/bluedynamics/cloud-vinyl",
        },
    ],
}

html_static_path = ["_static"]
