const WHATSAPP_NUMBER = "INSERIR_NUMERO_AQUI";
const WHATSAPP_MESSAGE = encodeURIComponent(
  "Olá! Gostaria de receber informações sobre a próxima temporada do Socializando."
);

const buildWhatsAppLink = () => {
  const normalized = WHATSAPP_NUMBER.replace(/\D/g, "");
  return normalized && normalized !== ""
    ? `https://wa.me/${normalized}?text=${WHATSAPP_MESSAGE}`
    : "#";
};

const bindWhatsAppLinks = () => {
  const href = buildWhatsAppLink();
  document.querySelectorAll("[data-whatsapp]").forEach((element) => {
    element.setAttribute("href", href);
    if (href === "#") {
      element.addEventListener("click", (event) => {
        event.preventDefault();
        alert("Inserir o número do WhatsApp em script.js antes da publicação.");
      });
    }
  });
};

const setupReveal = () => {
  const observer = new IntersectionObserver(
    (entries) => {
      entries.forEach((entry) => {
        if (entry.isIntersecting) {
          entry.target.classList.add("is-visible");
          observer.unobserve(entry.target);
        }
      });
    },
    { threshold: 0.18 }
  );

  document.querySelectorAll(".reveal").forEach((element) => {
    if (!element.classList.contains("reveal--immediate")) observer.observe(element);
  });
};

const setupHeader = () => {
  const header = document.querySelector("[data-header]");
  const onScroll = () => {
    header.classList.toggle("is-scrolled", window.scrollY > 18);
    const scrollable = document.documentElement.scrollHeight - window.innerHeight;
    const progress = scrollable > 0 ? (window.scrollY / scrollable) * 100 : 0;
    const bar = document.querySelector(".progress");
    if (bar) bar.style.width = `${progress}%`;

    document.querySelectorAll("[data-parallax]").forEach((shape) => {
      const speed = Number(shape.dataset.parallax || 0);
      const base = shape.dataset.baseTransform && shape.dataset.baseTransform !== "none"
        ? `${shape.dataset.baseTransform} `
        : "";
      shape.style.transform = `${base}translate3d(0, ${window.scrollY * speed}px, 0)`;
    });
  };
  onScroll();
  window.addEventListener("scroll", onScroll, { passive: true });
};

const setupTilt = () => {
  document.querySelectorAll(".tilt-card").forEach((card) => {
    card.addEventListener("mousemove", (event) => {
      if (window.innerWidth < 900) return;
      const rect = card.getBoundingClientRect();
      const px = (event.clientX - rect.left) / rect.width;
      const py = (event.clientY - rect.top) / rect.height;
      const rotateY = (px - 0.5) * 8;
      const rotateX = (0.5 - py) * 8;
      card.style.transform = `perspective(900px) rotateX(${rotateX}deg) rotateY(${rotateY}deg) translateY(-4px)`;
    });

    card.addEventListener("mouseleave", () => {
      card.style.transform = "";
    });
  });
};

window.addEventListener("load", () => {
  document.body.classList.add("loaded");
  bindWhatsAppLinks();
  setupReveal();
  setupHeader();
  setupTilt();
});
