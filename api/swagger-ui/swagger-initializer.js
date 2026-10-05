window.addEventListener("load", function () {
  window.ui = SwaggerUIBundle({
    url: "../openapi.json",
    dom_id: "#swagger-ui",
    presets: [SwaggerUIBundle.presets.apis],
    layout: "BaseLayout",
    deepLinking: true,
    displayRequestDuration: true,
    filter: true,
    docExpansion: "list",
    defaultModelsExpandDepth: 0,
    persistAuthorization: false,
    validatorUrl: null,
    supportedSubmitMethods: ["get", "post", "delete"],
    requestInterceptor: function (request) {
      // Swagger may apply both authorized alternatives to /v1/whoami.
      // The API accepts exactly one credential header; prefer Bearer in the UI.
      var names = Object.keys(request.headers || {});
      var bearer = names.some(function (name) {
        return name.toLowerCase() === "authorization";
      });
      if (bearer) {
        names.forEach(function (name) {
          if (name.toLowerCase() === "x-api-key") delete request.headers[name];
        });
      }
      return request;
    }
  });
});
