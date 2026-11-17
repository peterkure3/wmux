use serde::{Deserialize, Serialize};
#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub enum Axis {
    Columns,
    Rows,
}
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Rect {
    pub x: u16,
    pub y: u16,
    pub width: u16,
    pub height: u16,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum Node {
    Leaf(String),
    Split {
        axis: Axis,
        ratio: u16,
        a: Box<Node>,
        b: Box<Node>,
    },
}
impl Node {
    pub fn leaves(&self) -> Vec<String> {
        match self {
            Self::Leaf(id) => vec![id.clone()],
            Self::Split { a, b, .. } => [a.leaves(), b.leaves()].concat(),
        }
    }
    pub fn split(&mut self, id: &str, new: &str, axis: Axis) -> bool {
        match self {
            Self::Leaf(old) if old == id => {
                *self = Self::Split {
                    axis,
                    ratio: 500,
                    a: Box::new(self.clone()),
                    b: Box::new(Self::Leaf(new.into())),
                };
                true
            }
            Self::Split { a, b, .. } => a.split(id, new, axis) || b.split(id, new, axis),
            _ => false,
        }
    }
    pub fn remove(self, id: &str) -> Option<Self> {
        match self {
            Self::Leaf(ref s) if s == id => None,
            Self::Leaf(_) => Some(self),
            Self::Split { axis, ratio, a, b } => match (a.remove(id), b.remove(id)) {
                (Some(a), Some(b)) => Some(Self::Split {
                    axis,
                    ratio,
                    a: Box::new(a),
                    b: Box::new(b),
                }),
                (Some(a), None) | (None, Some(a)) => Some(a),
                _ => None,
            },
        }
    }
    /// Change the nearest ancestor divider of a leaf, clamped to 10–90%.
    pub fn resize(&mut self, id: &str, delta: i16) -> bool {
        match self {
            Self::Split { ratio, a, b, .. } => {
                if a.resize(id, delta) || b.resize(id, delta) {
                    true
                } else if a.leaves().iter().chain(b.leaves().iter()).any(|s| s == id) {
                    *ratio = (*ratio as i32 + delta as i32).clamp(100, 900) as u16;
                    true
                } else {
                    false
                }
            }
            _ => false,
        }
    }
    pub fn swap(&mut self, first: &str, second: &str) {
        match self {
            Self::Leaf(id) => {
                if id == first {
                    *id = second.into()
                } else if id == second {
                    *id = first.into()
                }
            }
            Self::Split { a, b, .. } => {
                a.swap(first, second);
                b.swap(first, second);
            }
        }
    }
    /// Children share their border cell. This produces connected zero-gap junctions.
    pub fn rects(&self, r: Rect) -> Vec<(String, Rect)> {
        match self {
            Self::Leaf(id) => vec![(id.clone(), r)],
            Self::Split { axis, ratio, a, b } => {
                let length = if *axis == Axis::Columns {
                    r.width
                } else {
                    r.height
                };
                if length < 3 {
                    return self.leaves().into_iter().map(|s| (s, r)).collect();
                }
                let cut = ((u32::from(length - 1) * u32::from(*ratio) / 1000) as u16)
                    .clamp(1, length - 2);
                let (mut ra, mut rb) = (r, r);
                if *axis == Axis::Columns {
                    ra.width = cut + 1;
                    rb.x += cut;
                    rb.width -= cut;
                } else {
                    ra.height = cut + 1;
                    rb.y += cut;
                    rb.height -= cut;
                }
                [a.rects(ra), b.rects(rb)].concat()
            }
        }
    }
    pub fn preset(ids: &[String], name: &str) -> Option<Self> {
        if ids.is_empty() {
            return None;
        }
        if ids.len() == 1 {
            return Some(Self::Leaf(ids[0].clone()));
        }
        let (axis, cut) = match name {
            "rows" => (Axis::Rows, ids.len() / 2),
            "columns" => (Axis::Columns, ids.len() / 2),
            "main-stack" => (Axis::Columns, 1),
            _ => (
                if ids.len() > 2 {
                    Axis::Rows
                } else {
                    Axis::Columns
                },
                ids.len() / 2,
            ),
        };
        let child_name = if name == "grid" {
            "columns"
        } else if name == "main-stack" {
            "rows"
        } else {
            name
        };
        Some(Self::Split {
            axis,
            ratio: 500,
            a: Box::new(Self::preset(&ids[..cut], child_name)?),
            b: Box::new(Self::preset(&ids[cut..], child_name)?),
        })
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn grid_has_shared_borders_at_all_sizes() {
        let n = Node::preset(&["a".into(), "b".into(), "c".into(), "d".into()], "grid").unwrap();
        for (w, h) in [(80, 24), (120, 40), (200, 60)] {
            let r = n.rects(Rect {
                x: 0,
                y: 0,
                width: w,
                height: h,
            });
            assert_eq!(r[0].1.x + r[0].1.width - 1, r[1].1.x);
            assert_eq!(r[0].1.y + r[0].1.height - 1, r[2].1.y);
            assert_eq!(r[3].1.x + r[3].1.width, w);
        }
    }
    #[test]
    fn edits_preserve_leaves() {
        let mut n = Node::Leaf("a".into());
        assert!(n.split("a", "b", Axis::Columns));
        assert!(n.resize("b", 1000));
        n.swap("a", "b");
        assert_eq!(n.leaves(), vec!["b", "a"]);
        assert_eq!(n.remove("b").unwrap().leaves(), vec!["a"]);
    }
}
