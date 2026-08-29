---
title: lerp
tagline: A TUI that runs coding agents over your Linear board.
install: brew install mattwalters/tap/lerp
---

{{< cast webm="casts/demo.webm" mp4="casts/demo.mp4"
         poster="posters/demo.png"
         title="The lerp board: tickets waiting on a human in the On you panel, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log"
         autoplay=true >}}

Lerp answers the two questions an operator actually has.

1. What's blocked on me?
2. Are the agents still working?

The model is small. Any Linear status can be a queue, and a queue's
consumer is a coding agent pointed at the ticket. The agent runs to
exit, and a clean exit moves the ticket to the next status. The ticket
is the message, never consumed, only moved. Chain a few queues and your
board is the pipeline.

<figure class="pipeline">
<svg viewBox="0 0 776 304" role="img" aria-label="Your Linear board with lerp overlaid. Tickets flow from Backlog into Planning, where a claude consumer runs and lerp moves the ticket on to Plan Review. You promote it to In Progress, where an antigravity consumer runs and lerp moves it to In Review. You take it to Done. The consumers and the moves lerp makes are highlighted; every other move is yours.">
  <defs>
    <marker id="arr" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" class="pl-arrowhead"/>
    </marker>
    <marker id="arrA" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
      <path d="M0 0 L8 4 L0 8 z" class="pl-arrowhead-lerp"/>
    </marker>
  </defs>
  <g class="pl-stub">
    <rect class="pl-col" x="1" y="6" width="120" height="96" rx="8"/>
    <circle class="pl-dot-backlog" cx="15" cy="24" r="4"/>
    <text class="pl-name" x="25" y="28.5">Backlog</text>
    <rect class="pl-card" x="9" y="38" width="104" height="24" rx="5"/>
    <rect class="pl-bar" x="18" y="48" width="48" height="4" rx="2"/>
    <rect class="pl-card" x="9" y="68" width="104" height="24" rx="5"/>
    <rect class="pl-bar" x="18" y="78" width="62" height="4" rx="2"/>
  </g>
  <g class="pl-stub">
    <rect class="pl-col" x="655" y="6" width="120" height="96" rx="8"/>
    <circle class="pl-dot-done" cx="669" cy="24" r="4.5"/>
    <text class="pl-name" x="679" y="28.5">Done</text>
    <rect class="pl-card" x="663" y="38" width="104" height="24" rx="5"/>
    <rect class="pl-bar" x="672" y="48" width="56" height="4" rx="2"/>
    <rect class="pl-card" x="663" y="68" width="104" height="24" rx="5"/>
    <rect class="pl-bar" x="672" y="78" width="42" height="4" rx="2"/>
  </g>
  <path class="pl-move-you" d="M 61 106 C 61 128, 91 122, 91 144" fill="none" marker-end="url(#arr)"/>
  <text class="pl-move-label-you" x="106" y="130">you</text>
  <path class="pl-move-you" d="M 685 146 C 685 124, 715 130, 715 108" fill="none" marker-end="url(#arr)"/>
  <text class="pl-move-label-you" x="670" y="130" text-anchor="end">you</text>
  <rect class="pl-col" x="12" y="150" width="158" height="110" rx="8"/>
  <circle class="pl-dot" cx="27" cy="168" r="4.5"/>
  <text class="pl-name" x="38" y="172.5">Planning</text>
  <rect class="pl-card pl-card-active" x="20" y="182" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="30" y="195" width="76" height="4.5" rx="2.25"/>
  <rect class="pl-card" x="20" y="218" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="30" y="231" width="52" height="4.5" rx="2.25"/>
  <line class="pl-tether" x1="91" y1="260" x2="91" y2="274"/>
  <rect class="pl-chip" x="51" y="274" width="80" height="24" rx="12"/>
  <text class="pl-chip-text" x="91" y="289.5">claude</text>
  <line class="pl-move-lerp" x1="174" y1="205" x2="204" y2="205" marker-end="url(#arrA)"/>
  <text class="pl-move-label-lerp" x="189" y="196">lerp</text>
  <rect class="pl-col" x="210" y="150" width="158" height="110" rx="8"/>
  <circle class="pl-dot" cx="225" cy="168" r="4.5"/>
  <text class="pl-name" x="236" y="172.5">Plan Review</text>
  <rect class="pl-card" x="218" y="182" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="228" y="195" width="84" height="4.5" rx="2.25"/>
  <rect class="pl-card" x="218" y="218" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="228" y="231" width="48" height="4.5" rx="2.25"/>
  <line class="pl-move-you" x1="372" y1="205" x2="402" y2="205" marker-end="url(#arr)"/>
  <text class="pl-move-label-you" x="387" y="196">you</text>
  <rect class="pl-col" x="408" y="150" width="158" height="110" rx="8"/>
  <circle class="pl-dot" cx="423" cy="168" r="4.5"/>
  <text class="pl-name" x="434" y="172.5">In Progress</text>
  <rect class="pl-card pl-card-active" x="416" y="182" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="426" y="195" width="68" height="4.5" rx="2.25"/>
  <rect class="pl-card" x="416" y="218" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="426" y="231" width="82" height="4.5" rx="2.25"/>
  <line class="pl-tether" x1="487" y1="260" x2="487" y2="274"/>
  <rect class="pl-chip" x="439" y="274" width="96" height="24" rx="12"/>
  <text class="pl-chip-text" x="487" y="289.5">antigravity</text>
  <line class="pl-move-lerp" x1="570" y1="205" x2="600" y2="205" marker-end="url(#arrA)"/>
  <text class="pl-move-label-lerp" x="585" y="196">lerp</text>
  <rect class="pl-col" x="606" y="150" width="158" height="110" rx="8"/>
  <circle class="pl-dot" cx="621" cy="168" r="4.5"/>
  <text class="pl-name" x="632" y="172.5">In Review</text>
  <rect class="pl-card" x="614" y="182" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="624" y="195" width="60" height="4.5" rx="2.25"/>
  <rect class="pl-card" x="614" y="218" width="142" height="30" rx="5"/>
  <rect class="pl-bar" x="624" y="231" width="44" height="4.5" rx="2.25"/>
</svg>
</figure>

Lerp is a reconciler, in the Kubernetes sense. On every pass it compares
the board with the agent processes on your machine, starts what is
missing, reaps what has finished, and adopts anything a previous lerp
left running.

The config for the board above is one file, checked into the repo.

{{< config-snippet "config" >}}

Everything runs on your machine. There is no server and no daemon, and
the only account involved is your Linear account. **Linear is the
database.** Every plan, decision, and status change lands there, and
lerp keeps nothing of its own but ephemeral local logs.

Lerp works with [Linear](https://linear.app) and nothing else. The agent
side is pluggable.

1. Claude Code
2. Codex
3. Antigravity

## Install

{{< install >}}

Check out the [docs](docs/install.md) for other install methods.

The thinking behind it is in [Why lerp](why.md).

<p class="epigraph"><a href="https://en.wikipedia.org/wiki/Linear_interpolation" class="headword">lerp</a> <span class="pron">/lərp/</span> <em>v.</em> To interpolate linearly; to move smoothly between two points.</p>

{{< cta "/docs/quickstart" >}}$ lerp init →{{< /cta >}}
