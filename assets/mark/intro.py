"""The presentation opener: a swarm of agents becomes the mark, the mark splits
into two, the visor boots, a hand-off runs down the cut, and the name arrives.

For presentations only. Everywhere else the mark is still: see BRAND.md.

    cd assets/mark && THEME=dark manim -qh intro.py Intro      # and THEME=light

Needs Manim Community 0.19+. The numbers come from geometry.py.
"""

import math
import os

from manim import (
    BLACK, DOWN, LEFT, ORIGIN, RIGHT, Circle, Create, Difference, Dot, FadeIn, FadeOut, Group,
    Intersection, LaggedStart, Line, Polygon, RoundedRectangle, Scene, Text, ValueTracker,
    VGroup, always_redraw, np, rate_functions,
)

from geometry import BOX, DARK, LIGHT, MARK, TEXT_DARK, TEXT_LIGHT

THEME = os.environ.get("THEME", "dark")
BG, FG, TEXT = {"dark": ("#1A2023", DARK, TEXT_DARK), "light": ("#FBF7EF", LIGHT, TEXT_LIGHT)}[THEME]

_, _, SIDE, CORNER = MARK["body"]
VISOR_W, VISOR_H, VISOR_R = MARK["visor"]
GUTTER = MARK["cut"]
NODE_R, NODE_AT = MARK["nodes"]
K = 5.6 / SIDE                                    # box units -> scene units
N = np.array([1, 1, 0]) / math.sqrt(2)            # normal to the cut; the scene's y points up
T = np.array([1, -1, 0]) / math.sqrt(2)           # along the cut, top-left to bottom-right
EASE = rate_functions.ease_out_cubic


def node_points():
    """Top-left first: the hand-off runs the way the cut is read."""
    return [T * s * NODE_AT * math.sqrt(2) * K for s in (-1, 1)]


def closed_halves():
    """The squircle cut in two, with no visor and no nodes, pushed together so it looks whole."""
    body, big = RoundedRectangle(width=SIDE * K, height=SIDE * K, corner_radius=CORNER * K), 20

    def side(sign):
        c = N * sign * (GUTTER / 2 * K)
        return Polygon(c + T * big, c - T * big, c - T * big + N * sign * big, c + T * big + N * sign * big)

    a, b = (Intersection(body, side(s)).set_fill(FG, 1).set_stroke(width=0) for s in (1, -1))
    a.shift(-N * GUTTER / 2 * K)
    b.shift(N * GUTTER / 2 * K)
    return a, b


class Intro(Scene):
    def construct(self):
        self.camera.background_color = BG
        rng = np.random.default_rng(11)
        outline = RoundedRectangle(width=SIDE * K, height=SIDE * K, corner_radius=CORNER * K)

        # The swarm: agents, and the edges between near neighbours.
        n = 28
        pts = [np.array([*rng.uniform(-3.6, 3.6, 2), 0]) * np.array([1.5, 0.95, 0]) for _ in range(n)]
        dots = VGroup(*[Dot(p, radius=0.055, color=FG) for p in pts])
        edges = VGroup()
        for i, p in enumerate(pts):
            for j in sorted(range(n), key=lambda j: np.linalg.norm(pts[j] - p))[1:3]:
                if i < j:
                    edges.add(Line(p, pts[j], stroke_width=1.4, color=FG, stroke_opacity=0.35))
        self.play(LaggedStart(*[FadeIn(d, scale=0.4) for d in dots], lag_ratio=0.025),
                  Create(edges, lag_ratio=0.02), run_time=0.9)

        # Converge: each agent takes a place on the outline, in angular order so no two cross.
        order = sorted(range(n), key=lambda i: math.atan2(pts[i][1], pts[i][0]))
        spot = {i: outline.point_from_proportion((k / n + 0.125) % 1) for k, i in enumerate(order)}
        self.play(*[dots[i].animate.move_to(spot[i]) for i in range(n)], FadeOut(edges),
                  run_time=1.0, rate_func=rate_functions.ease_in_out_cubic)

        # Form, then split into two agents.
        a, b = closed_halves()
        self.play(FadeIn(VGroup(a, b)), FadeOut(dots), run_time=0.35)
        self.play(a.animate.shift(N * GUTTER / 2 * K), b.animate.shift(-N * GUTTER / 2 * K),
                  run_time=0.45, rate_func=EASE)

        # The visor boots: redrawn at each height, so its corners never squash.
        h = ValueTracker(0.6)
        visor = always_redraw(lambda: RoundedRectangle(
            width=VISOR_W * K, height=h.get_value() * K, corner_radius=min(VISOR_R, h.get_value() / 2) * K,
            fill_color=BG, fill_opacity=1, stroke_width=0))
        self.add(visor)
        self.play(h.animate.set_value(VISOR_H), run_time=0.45, rate_func=EASE)
        visor.clear_updaters()

        # The hand-off: a node opens, a signal runs down the cut, the next node opens.
        first, second = node_points()

        def punch(p):
            hole = Circle(radius=NODE_R * K, fill_color=BG, fill_opacity=1, stroke_width=0).move_to(p).scale(0.01)
            self.add(hole)
            return hole.animate.scale(100)

        self.play(punch(first), run_time=0.22, rate_func=EASE)
        signal = Dot(first, radius=0.07, color=FG)
        self.add(signal)
        self.play(signal.animate.move_to(second), run_time=0.4, rate_func=rate_functions.ease_in_out_sine)
        self.play(punch(second), FadeOut(signal, scale=0.3), run_time=0.22, rate_func=EASE)

        # The name. Placed from the mark's own size: the group also holds helpers with no size of their own.
        mark = Group(*[m for m in self.mobjects if not isinstance(m, ValueTracker)])
        side, scale = SIDE * K, 0.42
        word = Text("openarity", font="Avenir Next", weight="MEDIUM", color=TEXT)
        word.scale_to_fit_height(side * scale * 0.42)
        gap = side * scale * 0.32
        left_x = -(side * scale + gap + word.width) / 2 + side * scale / 2
        self.play(mark.animate.scale(scale, about_point=ORIGIN).shift(RIGHT * left_x), run_time=0.6, rate_func=EASE)
        word.move_to(RIGHT * (left_x + side * scale / 2 + gap + word.width / 2))
        self.play(FadeIn(word, shift=LEFT * 0.2), run_time=0.5, rate_func=EASE)
        self.wait(1.4)
