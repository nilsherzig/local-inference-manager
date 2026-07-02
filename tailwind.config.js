/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./internal/web/templates/**/*.gohtml"],
  theme: {
    extend: {
      colors: {
        // Muted, dusty purple used as the single UI accent.
        mauve: {
          200: "#ddd0e6",
          300: "#c8b6d6",
          400: "#ab90c1",
          500: "#8d6fa4",
          600: "#735988",
        },
      },
    },
  },
  plugins: [],
};
