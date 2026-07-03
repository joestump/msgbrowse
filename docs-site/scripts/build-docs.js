#!/usr/bin/env node
/**
 * Build documentation content
 *
 * Orchestrates the transformation of OpenSpecs and ADRs
 * into Docusaurus-compatible MDX files.
 */

console.log('Building documentation content...\n');

// Build spec mapping first (needed by transforms)
require('./build-spec-mapping');

// Transform OpenSpecs
require('./transform-openspecs');

// Transform ADRs
require('./transform-adrs');

// Copy hand-authored product docs (docs-site/docs/ -> docs-generated/docs/)
require('./copy-product-docs');

// Generate index page
require('./generate-index');

// Generate graph page (artifact DAG from frontmatter, per ADR-0023 / SPEC-0018)
require('./generate-graph');

console.log('\nDocumentation content build complete!');
