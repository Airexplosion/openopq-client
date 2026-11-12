"use strict";

(() => {
  const gate = document.getElementById("auth-gate");
  const loginCanvas = document.getElementById("login-nebula-canvas");
  const overviewBanner = document.getElementById("overview-hero-banner");
  const overviewCanvas = document.getElementById("overview-particle-canvas");
  const shell = document.getElementById("app-shell");
  const root = document.documentElement;

  const heroTrigger = document.getElementById("hero-unlock-trigger");
  const heroTriggerLabel = document.getElementById("hero-unlock-label");
  const heroHint = document.getElementById("hero-auth-hint");
  const openWelcomeBtn = document.getElementById("open-welcome-hero");
  const motionButton = document.getElementById("login-motion-toggle");
  const motionLabel = document.getElementById("login-motion-label");
  const motionIcon = document.getElementById("login-motion-icon");
  const workspaceMotionBtn = document.getElementById("workspace-motion-toggle");

  if (!gate || !loginCanvas) return;

  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));
  const TAU = Math.PI * 2;

  let paused = false;
  let currentView = "overview";
  let showcaseActive = false;
  let showcaseReturnFocus = null;
  let rafId = 0;
  let lastStamp = 0;
  let clock = 0;

  const defaultHeroLabel = heroTriggerLabel ? heroTriggerLabel.textContent : "解锁进入工作台";
  const defaultHeroHintText = heroHint?.querySelector("span")?.textContent || "本地回环零信任保护 · 退出不断开 QQ";

  // Deterministic PRNG so particle distribution feels sculpted and balanced every load.
  function createRng(seedInit) {
    let seed = seedInit | 0;
    return () => {
      seed = (Math.imul(seed, 1664525) + 1013904223) | 0;
      return (seed >>> 0) / 4294967296;
    };
  }

  const PALETTES = {
    obsidian: {
      isLight: false,
      auraCore: "rgba(59, 130, 246, 0.085)",
      auraMid: "rgba(99, 102, 241, 0.034)",
      auraOuter: "rgba(236, 72, 153, 0.012)",
      crestSheen: [242, 249, 255],
      stops: [
        [56, 189, 248],  // Luminous sky cyan
        [59, 138, 252],  // Electric sapphire cobalt
        [78, 114, 255],  // Vibrant ultramarine
        [114, 96, 252],  // Royal indigo
        [168, 90, 250],  // Vivid iris amethyst
        [234, 74, 166],  // Luminous rose-magenta
        [250, 70, 114],  // Crisp coral-crimson
      ],
    },
    aurora: {
      isLight: false,
      auraCore: "rgba(34, 211, 238, 0.09)",
      auraMid: "rgba(59, 130, 246, 0.036)",
      auraOuter: "rgba(168, 85, 247, 0.014)",
      crestSheen: [236, 254, 255],
      stops: [
        [45, 212, 191],  // Aurora emerald-teal
        [34, 211, 238],  // Pure cyan stream
        [56, 189, 248],  // Sky sapphire
        [68, 140, 252],  // Electric cobalt
        [118, 102, 250], // Royal indigo
        [184, 108, 252], // Orchid violet
        [244, 106, 182], // Polar rose
      ],
    },
    daylight: {
      isLight: true,
      auraCore: "rgba(37, 99, 235, 0.045)",
      auraMid: "rgba(99, 102, 241, 0.018)",
      auraOuter: "rgba(225, 29, 72, 0.006)",
      crestSheen: null,
      stops: [
        [37, 99, 235],   // Antigravity electric cobalt
        [43, 92, 238],   // Pure sapphire blue
        [67, 72, 226],   // Crisp ultramarine
        [105, 58, 222],  // Royal indigo-violet
        [156, 48, 208],  // Vivid iris purple
        [214, 41, 118],  // Crisp rose-berry
        [225, 45, 85],   // Antigravity crimson-rose
      ],
    },
  };

  function getPalette() {
    const theme = root.dataset.theme || "obsidian";
    return PALETTES[theme] || PALETTES.obsidian;
  }

  function writeStopColor(stops, t, sheenRgb, sheenAmt, out) {
    const clamped = Math.max(0, Math.min(0.9999, t));
    const scaled = clamped * (stops.length - 1);
    const idx = Math.floor(scaled);
    const frac = scaled - idx;
    const a = stops[idx];
    const b = stops[Math.min(stops.length - 1, idx + 1)];
    let r = a[0] + (b[0] - a[0]) * frac;
    let g = a[1] + (b[1] - a[1]) * frac;
    let bl = a[2] + (b[2] - a[2]) * frac;
    if (sheenRgb && sheenAmt > 0) {
      r += (sheenRgb[0] - r) * sheenAmt;
      g += (sheenRgb[1] - g) * sheenAmt;
      bl += (sheenRgb[2] - bl) * sheenAmt;
    }
    out.cr = Math.round(r);
    out.cg = Math.round(g);
    out.cb = Math.round(bl);
  }

  class KineticVortexField {
    constructor(canvas, container, options) {
      this.canvas = canvas;
      this.container = container;
      this.ctx = canvas.getContext("2d", { alpha: true });
      this.kind = options.kind;
      this.count = options.count;
      this.speckleCount = options.speckleCount;
      this.rMin = options.rMin || 0.105;
      this.width = 1;
      this.height = 1;
      this.dpr = 1;
      this.focalX = options.focalX;
      this.targetFocalX = options.focalX;
      this.focalY = options.focalY;
      this.pointer = {
        active: false,
        x: 0,
        y: 0,
        smoothX: 0,
        smoothY: 0,
        vx: 0,
        vy: 0,
        smoothVX: 0,
        smoothVY: 0,
      };
      this.ripples = [];
      this.dashes = [];
      this.speckles = [];
      this.colorScratch = { cr: 0, cg: 0, cb: 0 };
      this.buildGeometry(options.seed);
    }

    buildGeometry(seed) {
      const rand = createRng(seed);
      this.dashes.length = 0;
      this.speckles.length = 0;

      // High-density Golden-Angle Phyllotaxis cloud with subtle blue-noise jitter:
      // Zero connecting lines — pure luminous dots & plump micro-droplets with rich, even spacing.
      const rSpan = 1.06 - this.rMin;
      for (let i = 0; i < this.count; i += 1) {
        const progress = (i + 0.5) / this.count;
        const radialBase = this.rMin + rSpan * Math.pow(progress, 0.61);
        const radialJitter = (rand() - 0.5) * 0.014;
        const radialNorm = Math.max(this.rMin * 0.92, radialBase + radialJitter);
        const normT = Math.max(0, Math.min(1, (radialNorm - this.rMin) / rSpan));
        const theta0 = i * GOLDEN_ANGLE + (rand() - 0.5) * 0.085;
        const envelope = Math.sin(Math.pow(normT, 0.76) * Math.PI);
        const radialTint = 0.38 + 0.62 * Math.pow(normT, 0.56);
        const scaleVar = 0.8 + rand() * 0.42;
        const phaseVar = rand() * TAU;

        this.dashes.push({
          radialNorm,
          normT,
          theta0,
          envelope,
          radialTint,
          scaleVar,
          phaseVar,
          ox: 0,
          oy: 0,
          vx: 0,
          vy: 0,
        });
      }

      // Multi-scale ambient stardust dots across negative space & outer halo
      for (let i = 0; i < this.speckleCount; i += 1) {
        this.speckles.push({
          u: rand(),
          v: rand(),
          depth: 0.35 + rand() * 0.65,
          radius: 0.75 + Math.pow(rand(), 1.5) * 1.45,
          tint: rand(),
          phase: rand() * TAU,
          speed: 0.28 + rand() * 0.52,
        });
      }
    }

    resize() {
      if (!this.ctx) return;
      const rect = this.container.getBoundingClientRect();
      this.width = Math.max(1, rect.width);
      this.height = Math.max(1, rect.height);
      const maxPixels = this.kind === "hero" ? 3800000 : 1800000;
      this.dpr = Math.min(2, window.devicePixelRatio || 1, Math.sqrt(maxPixels / (this.width * this.height)));
      const w = Math.max(1, Math.round(this.width * this.dpr));
      const h = Math.max(1, Math.round(this.height * this.dpr));
      if (this.canvas.width !== w || this.canvas.height !== h) {
        this.canvas.width = w;
        this.canvas.height = h;
      }
      this.ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
      if (!this.pointer.active) {
        this.pointer.smoothX = this.width * this.focalX;
        this.pointer.smoothY = this.height * this.focalY;
      }
    }

    addRipple(clientX, clientY) {
      const rect = this.container.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return;
      const x = clientX - rect.left;
      const y = clientY - rect.top;
      this.ripples.push({ x, y, radius: 10, strength: 1 });
      if (this.ripples.length > 4) this.ripples.shift();
    }

    onPointerMove(clientX, clientY) {
      const rect = this.container.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return;
      const nx = clientX - rect.left;
      const ny = clientY - rect.top;
      if (this.pointer.active) {
        this.pointer.vx = Math.max(-42, Math.min(42, nx - this.pointer.x));
        this.pointer.vy = Math.max(-42, Math.min(42, ny - this.pointer.y));
      }
      this.pointer.x = nx;
      this.pointer.y = ny;
      this.pointer.active = true;
    }

    onPointerLeave() {
      this.pointer.active = false;
      this.pointer.vx = 0;
      this.pointer.vy = 0;
    }

    render(dt, time) {
      const ctx = this.ctx;
      if (!ctx || this.width <= 1 || this.height <= 1) return;

      const palette = getPalette();
      const w = this.width;
      const h = this.height;
      const isHero = this.kind === "hero";
      const isUnlock = isHero && gate.dataset.gateStage === "unlock";
      const mobile = w <= 820;

      if (isHero) {
        this.targetFocalX = mobile ? 0.54 : isUnlock ? 0.44 : 0.67;
        this.focalY = mobile ? 0.38 : 0.5;
      } else {
        this.targetFocalX = w <= 620 ? 0.78 : 0.77;
        this.focalY = 0.52;
      }
      this.focalX += (this.targetFocalX - this.focalX) * Math.min(1, dt * 4.2);

      const cx = w * this.focalX;
      const cy = h * this.focalY;
      const fieldRadius = isHero
        ? Math.max(w * (mobile ? 0.68 : 0.48), Math.min(w * 0.6, h * 0.84))
        : Math.min(w * 0.46, Math.max(h * 0.92, 220));

      const targetPX = this.pointer.active ? this.pointer.x : cx;
      const targetPY = this.pointer.active ? this.pointer.y : cy;
      const ptrFollow = Math.min(1, dt * 10);
      this.pointer.smoothX += (targetPX - this.pointer.smoothX) * ptrFollow;
      this.pointer.smoothY += (targetPY - this.pointer.smoothY) * ptrFollow;
      this.pointer.smoothVX += (this.pointer.vx - this.pointer.smoothVX) * Math.min(1, dt * 12);
      this.pointer.smoothVY += (this.pointer.vy - this.pointer.smoothVY) * Math.min(1, dt * 12);
      this.pointer.vx *= Math.pow(0.04, dt);
      this.pointer.vy *= Math.pow(0.04, dt);

      ctx.clearRect(0, 0, w, h);

      // 1. Clean subtle depth halo behind the dot vortex (zero muddy fog)
      const glowR = fieldRadius * 0.94;
      const g1 = ctx.createRadialGradient(cx, cy, glowR * 0.06, cx, cy, glowR);
      g1.addColorStop(0, palette.auraCore);
      g1.addColorStop(0.46, palette.auraMid);
      g1.addColorStop(0.78, palette.auraOuter);
      g1.addColorStop(1, "rgba(0, 0, 0, 0)");
      ctx.fillStyle = g1;
      ctx.fillRect(0, 0, w, h);

      // Update click shockwave ripples
      for (let r = this.ripples.length - 1; r >= 0; r -= 1) {
        const rip = this.ripples[r];
        rip.radius += dt * (isHero ? 390 : 245);
        rip.strength *= Math.pow(0.16, dt);
        if (rip.strength < 0.02) this.ripples.splice(r, 1);
      }

      // 2. Crisp ambient stardust micro-dots across the space
      const rgbOut = this.colorScratch;
      const eyeClearR2 = Math.pow(fieldRadius * 0.14, 2);
      for (let i = 0; i < this.speckles.length; i += 1) {
        const s = this.speckles[i];
        const sx = ((s.u * w + Math.sin(time * 0.18 * s.speed + s.phase) * 11 * s.depth) % w + w) % w;
        const sy = ((s.v * h + Math.cos(time * 0.15 * s.speed + s.phase) * 9 * s.depth) % h + h) % h;
        const dxEye = sx - cx;
        const dyEye = sy - cy;
        if (dxEye * dxEye + dyEye * dyEye < eyeClearR2) continue;

        let copyDim = 1;
        if (isHero && !mobile && !isUnlock && sx < w * 0.34) {
          copyDim = 0.42 + 0.58 * (sx / (w * 0.34));
        } else if (!isHero && sx < w * 0.52) {
          copyDim = 0.14 + 0.86 * Math.max(0, (sx - w * 0.18) / (w * 0.34));
        }

        writeStopColor(palette.stops, s.tint * 0.85, null, 0, rgbOut);
        const pulse = 0.45 + 0.55 * Math.sin(time * s.speed + s.phase);
        const alpha = (palette.isLight ? 0.28 : 0.36) * s.depth * pulse * copyDim;
        ctx.fillStyle = `rgba(${rgbOut.cr},${rgbOut.cg},${rgbOut.cb},${alpha.toFixed(3)})`;
        ctx.beginPath();
        ctx.arc(sx, sy, s.radius, 0, TAU);
        ctx.fill();
      }

      // 3. Smooth 3D Surface Orientation & Autonomous Orbital Current
      const parallaxX = this.pointer.active ? ((this.pointer.smoothX - cx) / Math.max(320, w)) * 0.22 : 0;
      const parallaxY = this.pointer.active ? ((this.pointer.smoothY - cy) / Math.max(320, h)) * 0.18 : 0;
      const yaw = Math.cos(time * 0.2) * 0.11 + parallaxX;
      const pitch = -0.17 + Math.sin(time * 0.24) * 0.085 + parallaxY;
      const cosY = Math.cos(yaw);
      const sinY = Math.sin(yaw);
      const cosP = Math.cos(pitch);
      const sinP = Math.sin(pitch);

      const autoAngle = time * 0.46;
      const autoX = cx + Math.cos(autoAngle) * fieldRadius * 0.45;
      const autoY = cy + Math.sin(autoAngle * 0.86) * fieldRadius * 0.41;
      const autoR = fieldRadius * 0.44;
      const autoR2 = autoR * autoR;

      const magnetRadius = isHero ? 290 : 180;
      const magnetR2 = magnetRadius * magnetRadius;
      const globalSpin = time * 0.038;
      const warmFocusAngle = 1.15 + Math.sin(time * 0.16) * 0.18;

      // Pure dot & plump micro-droplet proportions (zero thin lines!)
      const maxSegLen = isHero ? 3.8 : 2.8;
      const minThick = isHero ? 2.35 : 1.9;
      const extraThick = isHero ? 2.3 : 1.75;
      const damp = Math.pow(0.035, dt);
      const springBlend = Math.min(1, dt * 9.5);

      ctx.lineCap = "round";

      for (let i = 0; i < this.count; i += 1) {
        const p = this.dashes[i];

        const twistWave =
          Math.sin(p.radialNorm * 4.4 - time * 1.05) * 0.062 +
          Math.cos(p.theta0 * 3 + time * 0.58) * 0.024 * p.envelope;
        const theta = p.theta0 + globalSpin + twistWave;

        const spiralArm = Math.sin(p.radialNorm * 8.4 - time * 1.48 - theta * 3.0);
        const crossHarmonic = Math.cos(p.radialNorm * 5.6 - time * 0.94 + theta * 2.0);
        const shimmer = Math.sin(time * 1.1 + p.phaseVar) * 0.08;
        const wave = 0.58 * spiralArm + 0.34 * crossHarmonic + shimmer;
        const wave01 = Math.max(0, Math.min(1, 0.5 + 0.5 * wave));

        const bellContour =
          1 +
          (Math.sin(theta * 3 - time * 0.64) * 0.05 +
            Math.cos(theta * 5 + time * 0.46) * 0.022 +
            wave * 0.04) *
            (0.35 + 0.65 * p.normT);
        const rActive = p.radialNorm * bellContour;
        const lx = Math.cos(theta) * rActive;
        const ly = Math.sin(theta) * rActive;
        const lz = Math.sin(p.normT * Math.PI) * 0.19 + wave * 0.12 * p.envelope;

        const x1 = lx * cosY + lz * sinY;
        const z1 = -lx * sinY + lz * cosY;
        const y2 = ly * cosP - z1 * sinP;
        const z2 = ly * sinP + z1 * cosP;

        const perspective = 1 / (1.36 - z2 * 0.34);
        const baseX = cx + x1 * fieldRadius * perspective;
        const baseY = cy + y2 * fieldRadius * perspective;

        let autoBoost = 0;
        if (!this.pointer.active) {
          const adx = baseX - autoX;
          const ady = baseY - autoY;
          const ad2 = adx * adx + ady * ady;
          if (ad2 < autoR2) {
            autoBoost = 0.5 * (1 + Math.cos((Math.PI * Math.sqrt(ad2)) / autoR)) * 0.28;
          }
        }

        let targetOX = 0;
        let targetOY = 0;
        let cursorBoost = 0;
        let flowAngle = 0;

        if (this.pointer.active) {
          const mdx = baseX - this.pointer.smoothX;
          const mdy = baseY - this.pointer.smoothY;
          const d2 = mdx * mdx + mdy * mdy;
          if (d2 < magnetR2 && d2 > 0.04) {
            const dist = Math.sqrt(d2);
            const falloff = 0.5 * (1 + Math.cos((Math.PI * dist) / magnetRadius));
            const smoothFalloff = falloff * falloff;
            const nx = mdx / dist;
            const ny = mdy / dist;
            const push = smoothFalloff * (isHero ? 24 : 14);
            const swirl = smoothFalloff * (isHero ? 13 : 8);
            targetOX += nx * push - ny * swirl + this.pointer.smoothVX * smoothFalloff * 0.55;
            targetOY += ny * push + nx * swirl + this.pointer.smoothVY * smoothFalloff * 0.55;
            cursorBoost = smoothFalloff;
            flowAngle = Math.atan2(
              ny * 0.72 + nx * 0.28 + this.pointer.smoothVY * 0.04,
              nx * 0.72 - ny * 0.28 + this.pointer.smoothVX * 0.04,
            );
          }
        }

        for (let r = 0; r < this.ripples.length; r += 1) {
          const rip = this.ripples[r];
          const rdx = baseX - rip.x;
          const rdy = baseY - rip.y;
          const rDist = Math.sqrt(rdx * rdx + rdy * rdy) || 1;
          const band = Math.abs(rDist - rip.radius);
          if (band < 86) {
            const k = 0.5 * (1 + Math.cos((Math.PI * band) / 86)) * rip.strength;
            targetOX += (rdx / rDist) * k * 21;
            targetOY += (rdy / rDist) * k * 21;
            cursorBoost = Math.min(1.2, cursorBoost + k * 0.78);
          }
        }

        p.vx = (p.vx + (targetOX - p.ox) * 24 * dt) * damp;
        p.vy = (p.vy + (targetOY - p.oy) * 24 * dt) * damp;
        p.ox += p.vx * dt * 10 + (targetOX - p.ox) * springBlend;
        p.oy += p.vy * dt * 10 + (targetOY - p.oy) * springBlend;

        const px = baseX + p.ox;
        const py = baseY + p.oy;
        if (px < -16 || px > w + 16 || py < -16 || py > h + 16) continue;

        let copyFeather = 1;
        if (isHero && !mobile && !isUnlock && px < w * 0.35) {
          copyFeather = 0.24 + 0.76 * Math.max(0, px / (w * 0.35));
        } else if (!isHero && px < w * 0.54) {
          copyFeather = 0.08 + 0.92 * Math.max(0, (px - w * 0.24) / (w * 0.3));
        }

        let angle = Math.atan2(py - cy, px - cx) + spiralArm * 0.22 * p.envelope;
        if (cursorBoost > 0.015) {
          const diff = Math.atan2(Math.sin(flowAngle - angle), Math.cos(flowAngle - angle));
          angle += diff * Math.min(0.62, cursorBoost * 0.54);
        }

        const depthScale = Math.max(0.74, Math.min(1.24, perspective * 1.1)) * p.scaleVar;
        const outerFade = p.normT > 0.84 ? Math.max(0.24, 1 - (p.normT - 0.84) / 0.2) : 1;
        const innerCalm = p.normT < 0.1 ? 0.46 + (p.normT / 0.1) * 0.54 : 1;
        const waveSculpt = Math.pow(wave01, 1.12);
        const energy = Math.min(
          1.2,
          (0.22 + 0.78 * waveSculpt) * (0.34 + 0.66 * p.envelope) + cursorBoost * 0.65 + autoBoost,
        );

        const segLen = (0.12 + Math.pow(energy, 1.3) * maxSegLen * innerCalm * outerFade) * depthScale;
        const thickness = (minThick + energy * extraThick * innerCalm) * depthScale * (0.8 + 0.2 * outerFade);

        const warmCos = 0.5 + 0.5 * Math.cos(theta - warmFocusAngle);
        const colorPos = Math.min(
          1,
          Math.max(0, Math.pow(warmCos, 1.56) * p.radialTint + wave * 0.05 + cursorBoost * 0.08),
        );
        const sheen = palette.crestSheen && energy > 0.64 ? Math.min(0.32, (energy - 0.64) * 0.46) : 0;
        writeStopColor(palette.stops, colorPos, palette.crestSheen, sheen, rgbOut);

        const baseAlpha = palette.isLight ? 0.94 : 0.97;
        const alphaFloor = palette.isLight ? 0.44 : 0.5;
        const alpha = Math.min(
          0.98,
          baseAlpha * (alphaFloor + energy * 0.52) * outerFade * (0.68 + innerCalm * 0.32) * copyFeather,
        );

        const halfLen = Math.max(0.08, segLen * 0.5);
        const dx = Math.cos(angle) * halfLen;
        const dy = Math.sin(angle) * halfLen;

        if (!palette.isLight && energy > 0.8 && copyFeather > 0.5) {
          const glowAlpha = Math.min(0.22, (energy - 0.8) * 0.42) * outerFade * copyFeather;
          ctx.strokeStyle = `rgba(${rgbOut.cr},${rgbOut.cg},${rgbOut.cb},${glowAlpha.toFixed(3)})`;
          ctx.lineWidth = thickness * 2.3;
          ctx.beginPath();
          ctx.moveTo(px - dx, py - dy);
          ctx.lineTo(px + dx, py + dy);
          ctx.stroke();
        }

        ctx.strokeStyle = `rgba(${rgbOut.cr},${rgbOut.cg},${rgbOut.cb},${alpha.toFixed(3)})`;
        ctx.lineWidth = thickness;
        ctx.beginPath();
        ctx.moveTo(px - dx, py - dy);
        ctx.lineTo(px + dx, py + dy);
        ctx.stroke();
      }
    }
  }

  const heroField = new KineticVortexField(loginCanvas, gate, {
    kind: "hero",
    count: 1680,
    speckleCount: 320,
    rMin: 0.105,
    focalX: 0.67,
    focalY: 0.5,
    seed: 90421,
  });

  const overviewField =
    overviewCanvas && overviewBanner
      ? new KineticVortexField(overviewCanvas, overviewBanner, {
          kind: "overview",
          count: 680,
          speckleCount: 110,
          rMin: 0.105,
          focalX: 0.77,
          focalY: 0.52,
          seed: 51827,
        })
      : null;

  function isGateVisible() {
    return !gate.hidden;
  }

  function isOverviewVisible() {
    return Boolean(
      overviewField &&
        gate.hidden &&
        shell &&
        !shell.hidden &&
        currentView === "overview",
    );
  }

  function shouldAnimate() {
    if (document.visibilityState === "hidden") return false;
    return isGateVisible() || isOverviewVisible();
  }

  function stopLoop() {
    if (rafId) {
      window.cancelAnimationFrame(rafId);
      rafId = 0;
    }
    lastStamp = 0;
  }

  function renderStaticSnapshot() {
    if (isGateVisible()) {
      heroField.resize();
      heroField.render(0.016, clock || 2.4);
    }
    if (isOverviewVisible()) {
      overviewField.resize();
      overviewField.render(0.016, clock || 2.4);
    }
  }

  function tick(stamp) {
    rafId = 0;
    if (!shouldAnimate() || paused || reduced.matches) return;
    const dt = lastStamp ? Math.min(0.05, Math.max(0.004, (stamp - lastStamp) / 1000)) : 0.016;
    lastStamp = stamp;
    clock += dt;

    if (isGateVisible()) {
      heroField.render(dt, clock);
    }
    if (isOverviewVisible()) {
      overviewField.render(dt, clock);
    }

    rafId = window.requestAnimationFrame(tick);
  }

  function syncEngine() {
    stopLoop();
    if (!shouldAnimate()) return;
    if (isGateVisible()) heroField.resize();
    if (isOverviewVisible()) overviewField.resize();
    renderStaticSnapshot();
    if (!paused && !reduced.matches) {
      lastStamp = performance.now();
      rafId = window.requestAnimationFrame(tick);
    }
  }

  function syncMotionControls() {
    const isStatic = reduced.matches;
    root.dataset.loginMotion = paused || isStatic ? "paused" : "playing";

    if (motionButton) {
      motionButton.disabled = isStatic;
      motionButton.setAttribute("aria-pressed", String(paused));
      motionButton.setAttribute("aria-label", paused ? "继续流光粒子动效" : "暂停流光粒子动效");
    }
    if (motionLabel) {
      motionLabel.textContent = isStatic ? "静态呈现" : paused ? "继续动效" : "暂停动效";
    }
    if (motionIcon) {
      motionIcon.setAttribute("d", paused ? "M6 4l5 4-5 4z" : "M5.5 4v8M10.5 4v8");
    }

    if (workspaceMotionBtn) {
      workspaceMotionBtn.disabled = isStatic;
      workspaceMotionBtn.setAttribute("aria-pressed", String(paused));
      workspaceMotionBtn.setAttribute("aria-label", paused ? "继续粒子动效" : "暂停粒子动效");
      workspaceMotionBtn.title = isStatic
        ? "系统已启用减少动态效果"
        : paused
          ? "继续粒子动效"
          : "暂停粒子动效";
      const span = workspaceMotionBtn.querySelector("span");
      if (span) span.textContent = isStatic ? "静态粒子" : paused ? "继续粒子" : "暂停粒子";
      const path = workspaceMotionBtn.querySelector("path");
      if (path) path.setAttribute("d", paused ? "M6 4l5 4-5 4z" : "M5.5 4v8M10.5 4v8");
    }

    syncEngine();
  }

  function restoreHeroCopy() {
    if (heroTriggerLabel) heroTriggerLabel.textContent = defaultHeroLabel;
    const hintSpan = heroHint?.querySelector("span");
    if (hintSpan) hintSpan.textContent = defaultHeroHintText;
  }

  function enterShowcase() {
    if (!shell || document.body.classList.contains("is-auth-locked")) return;
    showcaseActive = true;
    showcaseReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : openWelcomeBtn;
    gate.dataset.gateStage = "showcase";
    if (heroTriggerLabel) heroTriggerLabel.textContent = "返回工作台";
    const hintSpan = heroHint?.querySelector("span");
    if (hintSpan) hintSpan.textContent = "当前会话保持在线 · 点击按钮或按 Esc 返回工作台";
    shell.hidden = true;
    shell.inert = true;
    shell.setAttribute("aria-hidden", "true");
    gate.hidden = false;
    syncEngine();
    window.requestAnimationFrame(() => heroTrigger?.focus());
  }

  function exitShowcase() {
    if (!showcaseActive) return;
    showcaseActive = false;
    restoreHeroCopy();
    if (!document.body.classList.contains("is-auth-locked") && shell) {
      gate.hidden = true;
      gate.dataset.gateStage = "hero";
      shell.hidden = false;
      shell.inert = false;
      shell.removeAttribute("aria-hidden");
      syncEngine();
      window.requestAnimationFrame(() => showcaseReturnFocus?.focus());
    }
  }

  // Stage transitions: Hero <-> Frosted Unlock Card / Showcase
  heroTrigger?.addEventListener("click", (event) => {
    heroField.addRipple(event.clientX || heroField.width * 0.5, event.clientY || heroField.height * 0.5);
    if (showcaseActive || gate.dataset.gateStage === "showcase") {
      exitShowcase();
      return;
    }
    gate.dataset.gateStage = "unlock";
    const loginCard = document.getElementById("auth-login");
    const keyInput = document.getElementById("console-key");
    if (loginCard && !loginCard.hidden && keyInput) {
      window.setTimeout(() => keyInput.focus(), 80);
    }
  });

  document.querySelectorAll("[data-auth-back]").forEach((btn) => {
    btn.addEventListener("click", () => {
      if (showcaseActive || gate.dataset.gateStage === "showcase") {
        exitShowcase();
        return;
      }
      gate.dataset.gateStage = "hero";
      window.requestAnimationFrame(() => heroTrigger?.focus());
    });
  });

  openWelcomeBtn?.addEventListener("click", () => {
    enterShowcase();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || gate.hidden) return;
    if (showcaseActive || gate.dataset.gateStage === "showcase") {
      event.preventDefault();
      exitShowcase();
      return;
    }
    if (gate.dataset.gateStage === "unlock") {
      event.preventDefault();
      gate.dataset.gateStage = "hero";
      heroTrigger?.focus();
    }
  });

  // Pointer interactions on Hero & Overview Banner
  gate.addEventListener(
    "pointermove",
    (event) => {
      if (event.pointerType === "touch") return;
      heroField.onPointerMove(event.clientX, event.clientY);
    },
    { passive: true },
  );
  gate.addEventListener("pointerleave", () => heroField.onPointerLeave(), { passive: true });
  gate.addEventListener(
    "pointerdown",
    (event) => {
      if (event.target instanceof Element && event.target.closest("button, input, select, a, label, .auth-card")) {
        return;
      }
      heroField.addRipple(event.clientX, event.clientY);
      if (paused || reduced.matches) renderStaticSnapshot();
    },
    { passive: true },
  );

  if (overviewBanner && overviewField) {
    overviewBanner.addEventListener(
      "pointermove",
      (event) => {
        if (event.pointerType === "touch") return;
        overviewField.onPointerMove(event.clientX, event.clientY);
      },
      { passive: true },
    );
    overviewBanner.addEventListener("pointerleave", () => overviewField.onPointerLeave(), { passive: true });
    overviewBanner.addEventListener(
      "pointerdown",
      (event) => {
        if (event.target instanceof Element && event.target.closest("button, a")) return;
        overviewField.addRipple(event.clientX, event.clientY);
        if (paused || reduced.matches) renderStaticSnapshot();
      },
      { passive: true },
    );
  }

  motionButton?.addEventListener("click", () => {
    paused = !paused;
    syncMotionControls();
  });
  workspaceMotionBtn?.addEventListener("click", () => {
    paused = !paused;
    syncMotionControls();
  });

  // Segmented Settings Sub-Navigation
  const settingsView = document.querySelector('.settings-view[data-views="settings"]');
  const settingsTabs = Array.from(document.querySelectorAll(".settings-subnav [data-settings-tab]"));

  function activateSettingsTab(tabName) {
    if (!settingsView || !settingsTabs.length) return;
    const valid = settingsTabs.some((btn) => btn.dataset.settingsTab === tabName);
    const next = valid ? tabName : "transports";
    settingsView.dataset.settingsActive = next;
    settingsTabs.forEach((btn) => {
      const isSelected = btn.dataset.settingsTab === next;
      btn.classList.toggle("active", isSelected);
      btn.setAttribute("aria-selected", isSelected ? "true" : "false");
    });
  }

  settingsTabs.forEach((btn, index) => {
    btn.addEventListener("click", () => activateSettingsTab(btn.dataset.settingsTab));
    btn.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
      event.preventDefault();
      const delta = event.key === "ArrowRight" ? 1 : -1;
      const target = settingsTabs[(index + delta + settingsTabs.length) % settingsTabs.length];
      target.focus();
      activateSettingsTab(target.dataset.settingsTab);
    });
  });

  document.addEventListener("openopq:view", (event) => {
    currentView = event.detail?.view || "overview";
    if (shell) shell.dataset.activeView = currentView;
    syncEngine();
  });
  document.addEventListener("openopq:theme", () => syncEngine());
  document.addEventListener("visibilitychange", () => syncEngine());
  reduced.addEventListener("change", () => syncMotionControls());
  window.addEventListener("resize", () => syncEngine(), { passive: true });
  window.addEventListener("pagehide", () => stopLoop(), { passive: true });
  window.addEventListener("pageshow", () => syncEngine(), { passive: true });

  new MutationObserver(() => {
    if (document.body.classList.contains("is-auth-locked") && showcaseActive) {
      showcaseActive = false;
      restoreHeroCopy();
    }
    syncEngine();
  }).observe(gate, { attributes: true, attributeFilter: ["hidden", "data-gate-stage"] });

  if (shell) {
    new MutationObserver(() => syncEngine()).observe(shell, {
      attributes: true,
      attributeFilter: ["hidden"],
    });
  }

  if (location.hash) {
    const hashView = location.hash.replace(/^#/, "").trim();
    if (hashView) currentView = hashView;
  }
  if (shell) shell.dataset.activeView = currentView;

  gate.dataset.sceneReady = "true";
  syncMotionControls();
})();
