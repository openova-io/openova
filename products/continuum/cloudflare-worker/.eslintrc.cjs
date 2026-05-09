// .eslintrc.cjs — minimal ESLint config for the lease-witness Worker.
//
// Aligns with the @typescript-eslint/recommended ruleset plus a few
// adjustments for Workers idioms:
//   - `no-console` off — Workers Logs uses console.log
//   - `@typescript-eslint/no-explicit-any` warn (we cast a few times
//     for the Cloudflare KV types)
//   - allow underscore-prefixed unused params (Worker handlers have
//     unused `request` / `ctx` for some endpoints)

/* eslint-env node */
module.exports = {
  root: true,
  parser: "@typescript-eslint/parser",
  parserOptions: {
    ecmaVersion: 2022,
    sourceType: "module",
    project: "./tsconfig.json",
  },
  plugins: ["@typescript-eslint"],
  extends: [
    "eslint:recommended",
    "plugin:@typescript-eslint/recommended",
  ],
  env: {
    es2022: true,
    worker: true,
  },
  ignorePatterns: ["dist/", ".wrangler/", "node_modules/"],
  rules: {
    "no-console": "off",
    "@typescript-eslint/no-explicit-any": "warn",
    "@typescript-eslint/no-unused-vars": [
      "error",
      { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
    ],
  },
};
