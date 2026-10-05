# Bundled Swagger UI

`swagger-ui-bundle.js` and `swagger-ui.css` come from the official
`swagger-ui-dist` npm package, pinned to **5.33.1**. `LICENSE` and `NOTICE` are
included from that distribution. Other files in this directory belong to this
project and configure its documentation UI.

Source: https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-5.33.1.tgz

Package SHA-512 integrity:
`H872wWkA53bFIsGgi7OWgmq+CRWw3nFQGdJWRqOB9wNwTm6e5ol34+qPDkV4AJlK+gglM2EsJiOOzsGsEGbluA==`

The Go server embeds these files. No npm install, CDN, or online specification
validator is required at runtime. When updating, download a pinned official
distribution, verify its package integrity, replace the vendor files and license
notices, and update this version and checksum.
