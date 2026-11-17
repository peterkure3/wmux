//! Compact, bounded run-length encoding of identical screen cells.
use crate::Cell;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
pub fn serialize<S: Serializer>(rows: &[Vec<Cell>], serializer: S) -> Result<S::Ok, S::Error> {
    let runs: Vec<Vec<(u16, &Cell)>> = rows
        .iter()
        .map(|row| {
            let mut runs: Vec<(u16, &Cell)> = Vec::new();
            for cell in row {
                if let Some((n, last)) = runs.last_mut()
                    && *last == cell
                    && *n < u16::MAX
                {
                    *n += 1;
                    continue;
                }
                runs.push((1, cell));
            }
            runs
        })
        .collect();
    runs.serialize(serializer)
}
pub fn deserialize<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Vec<Vec<Cell>>, D::Error> {
    let runs = Vec::<Vec<(u16, Cell)>>::deserialize(deserializer)?;
    if runs.len() > 150 {
        return Err(serde::de::Error::custom("too many terminal rows"));
    }
    runs.into_iter()
        .map(|row| {
            let length: usize = row.iter().map(|(n, _)| *n as usize).sum();
            if length > 400 {
                return Err(serde::de::Error::custom("too many terminal columns"));
            }
            let mut result = Vec::with_capacity(length);
            for (n, cell) in row {
                if cell.text.len() > 32 {
                    return Err(serde::de::Error::custom("terminal cell too long"));
                }
                result.extend(std::iter::repeat_n(cell, n as usize));
            }
            Ok(result)
        })
        .collect()
}
