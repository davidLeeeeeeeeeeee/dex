// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/token/ERC20/extensions/ERC20Permit.sol";

contract MyToken is ERC20, ERC20Permit {
    constructor(address to1, uint256 amount1, address to2, uint256 amount2)
    ERC20("MyToken", "MTK")
    ERC20Permit("MyToken")
    {
        _mint(to1, amount1);
        _mint(to2, amount2);
    }

}
